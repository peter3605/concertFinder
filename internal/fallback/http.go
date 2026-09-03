package fallback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temoto/robotstxt"

	"github.com/peterho/concertfinder/internal/db"
)

const (
	// UserAgent is a last-resort default for a Fetcher built with an empty
	// string, matching how NewMusicBrainzClient handles the same case.
	// main.go builds the real one from SITE_BASE_URL + CONTACT_EMAIL and
	// passes it in; a User-Agent naming a repository that does not exist
	// satisfies the letter of every crawling policy and none of their purpose,
	// which is that the site operator can reach whoever is fetching them.
	UserAgent = "ConcertFinder/1.0 (+https://concertfinder.app)"

	minInterval = 3 * time.Second
	pageTTL     = 12 * time.Hour
)

// ErrDisallowed indicates robots.txt or the app-level blocklist rejected the URL.
var ErrDisallowed = errors.New("fallback: fetch disallowed")

// ErrUnsafeURL indicates the URL, or an address it resolves to, is not
// somewhere this process is willing to send a request.
//
// The URLs reaching this package come from MusicBrainz "official homepage"
// relationships, and MusicBrainz is a wiki anyone can edit. So the artist-site
// fetcher is a request-forger the moment it will follow any scheme to any
// address: without this it would fetch http://169.254.169.254/... on an EC2
// instance, or anything else inside the VPC, and — worse than merely fetching
// it — write the body into concert_cache, where it is served back for pageTTL.
var ErrUnsafeURL = errors.New("fallback: refusing to fetch this URL")

// blockedHosts is a permanent exclusion list — hosts whose Terms of Service
// prohibit automated access. DICE.fm is explicit in design §5.4.3.
var blockedHosts = map[string]bool{
	"dice.fm":     true,
	"www.dice.fm": true,
}

// hostGapCacheSize bounds the per-host rate-limiter map. It used to be a plain
// map in a process that runs for weeks between deploys, so it grew by one
// entry for every host any artist has ever linked to and never shrank.
//
// No TTL, unlike the robots cache below, and that is not an omission: the
// value is the timestamp of our last request to that host, and an entry older
// than minInterval already imposes no wait. Expiry would delete entries that
// are already inert.
const hostGapCacheSize = 2000

// Fetcher is a rate-limited, robots-aware, cache-backed HTTP GET client.
type Fetcher struct {
	HTTP *http.Client
	Pool *pgxpool.Pool // for page cache in concert_cache

	// UA identifies us to every site we fetch, including robots.txt, and is
	// the agent string robots rules are matched against.
	UA    string
	limMu sync.Mutex
	last  *lruCache // host -> time.Time of our last request
	rob   *robotsCache
}

func NewFetcher(pool *pgxpool.Pool, userAgent string) *Fetcher {
	if userAgent == "" {
		userAgent = UserAgent
	}
	// The guarded transport rather than the default one, so every request this
	// client makes — page fetches, robots.txt, and each hop of a redirect
	// chain — has its destination checked before the connection opens.
	return &Fetcher{
		HTTP: &http.Client{Timeout: 20 * time.Second, Transport: newGuardedTransport()},
		Pool: pool,
		UA:   userAgent,
		last: newLRU(hostGapCacheSize),
		rob:  newRobotsCache(),
	}
}

// GetPage returns HTML (or JSON, or anything) for a URL, honoring cache,
// blocklist, robots.txt, and the per-host min interval.
func (f *Fetcher) GetPage(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	// https only. Not a purity argument: the alternative to an allowlist is
	// that url.Parse happily accepts file:// and gopher://, and the one after
	// that is whatever scheme a MusicBrainz editor decides to type. Plain http
	// is refused with the rest because a downgrade is a fetch nobody can vouch
	// for, and the pages worth reading here all serve TLS.
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, fmt.Errorf("%w: scheme %q", ErrUnsafeURL, u.Scheme)
	}
	host := strings.ToLower(u.Host)
	if host == "" {
		return nil, fmt.Errorf("%w: no host", ErrUnsafeURL)
	}
	if blockedHosts[host] {
		return nil, ErrDisallowed
	}

	key := "page:" + rawURL
	if blob, ok, err := db.GetCachedConcerts(ctx, f.Pool, key, pageTTL); err == nil && ok {
		// Cached bytes are stored JSON-encoded (HTML isn't valid JSON but
		// concert_cache.results is jsonb). Decode; on corruption fall through
		// to re-fetch.
		var s string
		if json.Unmarshal(blob, &s) == nil {
			return []byte(s), nil
		}
	}

	allowed, err := f.rob.allowed(ctx, f.HTTP, u, f.UA)
	if err != nil {
		// Fail closed: if we can't read robots, don't fetch.
		return nil, fmt.Errorf("robots check: %w", err)
	}
	if !allowed {
		return nil, ErrDisallowed
	}

	if err := f.wait(ctx, host); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.UA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.5")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fetch %s: %s", rawURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	// concert_cache.results is jsonb, so wrap the raw HTML in a JSON string.
	// json.Marshal replaces invalid UTF-8 with U+FFFD, which is acceptable
	// for HTML in practice (most pages are UTF-8 already).
	if encoded, err := json.Marshal(string(body)); err == nil {
		_ = db.SaveCachedConcerts(ctx, f.Pool, key, encoded)
	}
	return body, nil
}

func (f *Fetcher) wait(ctx context.Context, host string) error {
	// limMu is still held around the LRU's own Get/Set pair: the read and the
	// write have to be one step, or two goroutines for the same host both see
	// the old timestamp and both go straight through.
	f.limMu.Lock()
	var last time.Time
	if v, ok := f.last.Get(host); ok {
		last, _ = v.(time.Time)
	}
	now := time.Now()
	nextAllowed := last.Add(minInterval)
	if nextAllowed.After(now) {
		f.last.Set(host, nextAllowed)
		f.limMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nextAllowed.Sub(now)):
			return nil
		}
	}
	f.last.Set(host, now)
	f.limMu.Unlock()
	return nil
}

// --- outbound address guard ---

// cgnatV4 is 100.64.0.0/10 (RFC 6598), the carrier-grade NAT range. net.IP
// has no method for it and IsPrivate does not cover it, but it is routable
// inside plenty of hosting networks, so it belongs with the rest.
var cgnatV4 = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isPublicIP reports whether we are willing to open a connection to ip.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// Normalize ::ffff:a.b.c.d to its 4-byte form, or an IPv4 address dressed
	// as IPv6 sidesteps every v4 test below.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	// IsUnspecified is 0.0.0.0 and ::, which most stacks route to this host.
	// IsPrivate is RFC 1918 and, per its own documentation, RFC 4193
	// fc00::/7. IsLinkLocalUnicast is what covers 169.254.169.254, the cloud
	// instance-metadata address — the single most valuable thing an SSRF here
	// could reach.
	switch {
	case ip.IsUnspecified(),
		ip.IsLoopback(),
		ip.IsPrivate(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast():
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		if cgnatV4.Contains(ip4) {
			return false
		}
		// 0.0.0.0/8 as a whole, not just the unspecified address: several
		// stacks route 0.x.y.z to the local host.
		return ip4[0] != 0
	}
	// fc00::/7 again, spelled out. IsPrivate covers it today; stating it here
	// means the guard does not silently narrow if that method's definition
	// ever does.
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return true
}

// newGuardedTransport builds a Transport that refuses to connect to any
// address outside public routable space.
//
// The check lives in DialContext rather than in a pre-flight parse of the URL
// for two reasons, and both are the difference between a guard and a
// decoration. Redirects: a pre-flight check sees only the first URL, and
// http.Client follows up to ten more without asking. And DNS rebinding: a
// hostname that resolved to a public address a moment ago can resolve to
// 127.0.0.1 when the connection is actually made. Resolving here and dialing
// the resolved literal closes that window — the address that was checked is
// the address that gets dialled.
func newGuardedTransport() *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		lastErr := fmt.Errorf("%w: %s resolved to nothing", ErrUnsafeURL, host)
		for _, ip := range ips {
			if !isPublicIP(ip.IP) {
				lastErr = fmt.Errorf("%w: %s resolves to %s", ErrUnsafeURL, host, ip.IP)
				continue
			}
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return t
}

// --- robots.txt cache ---

const (
	// robotsCacheSize bounds the number of hosts whose robots.txt we hold.
	robotsCacheSize = 1000

	// robotsTTL is how long a parsed robots.txt is trusted. The LRU bound
	// alone is not expiry: at 1000 slots against the handful of hosts a scan
	// actually touches, nothing is ever evicted, so a site that adds a
	// Disallow would keep being crawled for the life of the process — which,
	// between deploys, is weeks. Same shape as the trap mbCacheEntry.resolvedAt
	// exists for.
	robotsTTL = 12 * time.Hour
)

type robotsEntry struct {
	data      *robotstxt.RobotsData
	fetchedAt time.Time
}

func (e robotsEntry) live() bool { return time.Since(e.fetchedAt) < robotsTTL }

type robotsCache struct {
	entries *lruCache // host -> robotsEntry
}

func newRobotsCache() *robotsCache {
	return &robotsCache{entries: newLRU(robotsCacheSize)}
}

// allowed reports whether robots.txt permits ua to fetch u. A concurrent miss
// for the same host may fetch robots.txt twice; that is cheaper than holding a
// lock across a network round trip, and the second write is idempotent.
func (c *robotsCache) allowed(ctx context.Context, client *http.Client, u *url.URL, ua string) (bool, error) {
	host := strings.ToLower(u.Host)
	if v, ok := c.entries.Get(host); ok {
		if e, ok := v.(robotsEntry); ok && e.live() {
			return e.data.TestAgent(u.Path, ua), nil
		}
	}
	r, err := fetchRobots(ctx, client, u, ua)
	if err != nil {
		return false, err
	}
	c.entries.Set(host, robotsEntry{data: r, fetchedAt: time.Now()})
	return r.TestAgent(u.Path, ua), nil
}

func fetchRobots(ctx context.Context, client *http.Client, u *url.URL, ua string) (*robotstxt.RobotsData, error) {
	robotsURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		// No robots.txt reachable — treat as allow-all (robots convention).
		// This is also the path a blocked address takes, which is harmless:
		// the page fetch that follows uses the same guarded client and is
		// refused there.
		return robotstxt.FromStatusAndBytes(404, nil)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return robotstxt.FromStatusAndBytes(resp.StatusCode, body)
}
