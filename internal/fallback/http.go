package fallback

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// UserAgent identifies our traffic per §5.4.3. The contact URL is a
	// placeholder until a public deployment exists.
	UserAgent = "ConcertFinderBot/0.1 (+https://github.com/peterho/concertFinder)"

	minInterval = 3 * time.Second
	pageTTL     = 12 * time.Hour
)

// ErrDisallowed indicates robots.txt or the app-level blocklist rejected the URL.
var ErrDisallowed = errors.New("fallback: fetch disallowed")

// blockedHosts is a permanent exclusion list — hosts whose Terms of Service
// prohibit automated access. DICE.fm is explicit in design §5.4.3.
var blockedHosts = map[string]bool{
	"dice.fm":     true,
	"www.dice.fm": true,
}

// Fetcher is a rate-limited, robots-aware, cache-backed HTTP GET client.
type Fetcher struct {
	HTTP  *http.Client
	Pool  *pgxpool.Pool // for page cache in concert_cache
	limMu sync.Mutex
	last  map[string]time.Time
	rob   *robotsCache
}

func NewFetcher(pool *pgxpool.Pool) *Fetcher {
	return &Fetcher{
		HTTP:  &http.Client{Timeout: 20 * time.Second},
		Pool:  pool,
		last:  map[string]time.Time{},
		rob:   newRobotsCache(),
	}
}

// GetPage returns HTML (or JSON, or anything) for a URL, honoring cache,
// blocklist, robots.txt, and the per-host min interval.
func (f *Fetcher) GetPage(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := strings.ToLower(u.Host)
	if blockedHosts[host] {
		return nil, ErrDisallowed
	}

	key := "page:" + rawURL
	if blob, ok, err := db.GetCachedConcerts(ctx, f.Pool, key, pageTTL); err == nil && ok {
		return blob, nil
	}

	allowed, err := f.rob.allowed(ctx, f.HTTP, u)
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
	req.Header.Set("User-Agent", UserAgent)
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
	_ = db.SaveCachedConcerts(ctx, f.Pool, key, body)
	return body, nil
}

func (f *Fetcher) wait(ctx context.Context, host string) error {
	f.limMu.Lock()
	last := f.last[host]
	now := time.Now()
	nextAllowed := last.Add(minInterval)
	if nextAllowed.After(now) {
		f.last[host] = nextAllowed
		f.limMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nextAllowed.Sub(now)):
			return nil
		}
	}
	f.last[host] = now
	f.limMu.Unlock()
	return nil
}

// --- robots.txt cache ---

type robotsCache struct {
	mu   sync.Mutex
	data map[string]*robotstxt.RobotsData
}

func newRobotsCache() *robotsCache {
	return &robotsCache{data: map[string]*robotstxt.RobotsData{}}
}

func (c *robotsCache) allowed(ctx context.Context, client *http.Client, u *url.URL) (bool, error) {
	host := strings.ToLower(u.Host)
	c.mu.Lock()
	r, ok := c.data[host]
	c.mu.Unlock()
	if !ok {
		var err error
		r, err = fetchRobots(ctx, client, u)
		if err != nil {
			return false, err
		}
		c.mu.Lock()
		c.data[host] = r
		c.mu.Unlock()
	}
	return r.TestAgent(u.Path, UserAgent), nil
}

func fetchRobots(ctx context.Context, client *http.Client, u *url.URL) (*robotstxt.RobotsData, error) {
	robotsURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		// No robots.txt reachable — treat as allow-all (robots convention).
		return robotstxt.FromStatusAndBytes(404, nil)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return robotstxt.FromStatusAndBytes(resp.StatusCode, body)
}
