package fallback

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The URLs this fetcher follows come from MusicBrainz "official homepage"
// relationships, and MusicBrainz is a wiki anyone can edit. A scheme allowlist
// is the cheap half of not turning that into a request forger — url.Parse is
// perfectly happy with file:// and gopher://.
//
// Pool is deliberately nil: the scheme check runs before the page-cache read,
// so reaching the database at all would panic and fail the test rather than
// quietly passing.
func TestGetPageRejectsNonHTTPSSchemes(t *testing.T) {
	f := NewFetcher(nil, "ConcertFinder/1.0 (+https://example.test; ops@example.test)")
	for _, raw := range []string{
		"http://artist.example/tour",
		"file:///etc/passwd",
		"gopher://artist.example/",
		"ftp://artist.example/tour",
	} {
		if _, err := f.GetPage(context.Background(), raw); !errors.Is(err, ErrUnsafeURL) {
			t.Errorf("GetPage(%q) error = %v, want ErrUnsafeURL", raw, err)
		}
	}
}

// The expensive half. A hostname resolving into private space is an ordinary
// DNS record, so the refusal has to happen at connect time — and it has to be
// a refusal rather than a fetch, because GetPage writes what it reads into
// concert_cache and serves it back for pageTTL.
func TestGuardedTransportRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this must never be read"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: newGuardedTransport(), Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("dialled %s; the guard must refuse loopback", srv.URL)
	}
	if !errors.Is(err, ErrUnsafeURL) {
		t.Errorf("error = %v, want ErrUnsafeURL", err)
	}
}

func TestIsPublicIP(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
		why  string
	}{
		{"93.184.216.34", true, "an ordinary public v4 address"},
		{"2606:2800:220:1:248:1893:25c8:1946", true, "an ordinary public v6 address"},
		{"127.0.0.1", false, "loopback"},
		{"::1", false, "v6 loopback"},
		{"10.1.2.3", false, "RFC 1918"},
		{"192.168.0.1", false, "RFC 1918"},
		{"172.16.5.4", false, "RFC 1918"},
		{"169.254.169.254", false, "the cloud instance-metadata address"},
		{"0.0.0.0", false, "unspecified"},
		{"0.1.2.3", false, "0.0.0.0/8 routes to this host on several stacks"},
		{"100.64.1.1", false, "RFC 6598 carrier-grade NAT; IsPrivate does not cover it"},
		{"fd00::1", false, "RFC 4193 unique-local"},
		{"fc00::1", false, "RFC 4193 unique-local"},
		{"fe80::1", false, "v6 link-local"},
		{"224.0.0.1", false, "multicast"},
		// An IPv4 address wearing an IPv6 costume. Without the To4()
		// normalization every v4 test below it is skipped and this reaches
		// the dialler.
		{"::ffff:127.0.0.1", false, "v4-mapped loopback"},
		{"::ffff:10.0.0.1", false, "v4-mapped RFC 1918"},
	} {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("test data: %q is not an IP", tc.ip)
		}
		if got := isPublicIP(ip); got != tc.want {
			t.Errorf("isPublicIP(%s) = %v, want %v (%s)", tc.ip, got, tc.want, tc.why)
		}
	}
}

// The User-Agent is a contract with the sites we fetch, not a formality:
// MusicBrainz 403s anonymous traffic and Nominatim's stated remedy for a UA it
// cannot act on is a block. main.go builds one from the deployment's own base
// URL and contact address; this is the plumbing that carries it.
func TestNewFetcherUsesTheSuppliedUserAgent(t *testing.T) {
	const ua = "ConcertFinder/1.0 (+https://example.test; ops@example.test)"
	if got := NewFetcher(nil, ua).UA; got != ua {
		t.Errorf("UA = %q, want %q", got, ua)
	}
	if got := NewFetcher(nil, "").UA; got != UserAgent {
		t.Errorf("empty UA fell back to %q, want the package default %q", got, UserAgent)
	}
	// The default's whole job is to be reachable. The one this replaced named
	// a GitHub repository that does not exist, which satisfies the letter of
	// every crawling policy and none of its purpose.
	if !strings.Contains(UserAgent, "concertfinder.app") {
		t.Errorf("default UA %q names no address anyone can actually reach", UserAgent)
	}
}

// robots.txt is fetched with the same agent string the rules are then matched
// against, so a site that names us in a Disallow gets to be obeyed.
func TestRobotsFetchCarriesTheUserAgent(t *testing.T) {
	const ua = "ConcertFinder/1.0 (+https://example.test; ops@example.test)"
	var got string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	}))
	defer srv.Close()

	// srv.Client() rather than the Fetcher's own: the address guard exists to
	// refuse exactly the loopback address httptest listens on.
	u, err := url.Parse(srv.URL + "/tour")
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	c := newRobotsCache()
	allowed, err := c.allowed(context.Background(), srv.Client(), u, ua)
	if err != nil {
		t.Fatalf("allowed: %v", err)
	}
	if !allowed {
		t.Error("Allow: / was read as a disallow")
	}
	if got != ua {
		t.Errorf("robots.txt request User-Agent = %q, want %q", got, ua)
	}
}

// A cache bound is not an expiry. At 1000 slots against the handful of hosts a
// scan touches nothing is ever evicted, so a site that adds a Disallow would
// keep being crawled for the life of a process that runs for weeks.
func TestRobotsEntryExpires(t *testing.T) {
	fresh := robotsEntry{fetchedAt: time.Now()}
	if !fresh.live() {
		t.Error("a just-fetched robots.txt should be trusted")
	}
	stale := robotsEntry{fetchedAt: time.Now().Add(-robotsTTL - time.Minute)}
	if stale.live() {
		t.Error("robots.txt past its TTL must be re-fetched")
	}
}

// The per-host rate-limiter map used to be a plain map in a process that runs
// for weeks: one entry per host any artist has ever linked to, never removed.
func TestPerHostGapMapStaysBounded(t *testing.T) {
	f := NewFetcher(nil, "ua")
	ctx := context.Background()
	for i := 0; i < hostGapCacheSize+500; i++ {
		// A host we have never seen has a zero last-request time, so the gap
		// has long since elapsed and wait returns without sleeping.
		if err := f.wait(ctx, fmt.Sprintf("host%d.example", i)); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}
	if n := f.last.Len(); n > hostGapCacheSize {
		t.Errorf("per-host map holds %d entries, cap is %d", n, hostGapCacheSize)
	}
}

// A client built by hand (as the Songkick tests do) must still send something,
// and one built by main.go must send what main.go decided.
func TestSongkickUserAgent(t *testing.T) {
	if got := NewSongkickClient("k", "").UserAgent; got != songkickUserAgent {
		t.Errorf("empty UA = %q, want the package default", got)
	}
	const ua = "ConcertFinder/1.0 (+https://example.test; ops@example.test)"
	if got := NewSongkickClient("k", ua).UserAgent; got != ua {
		t.Errorf("UA = %q, want %q", got, ua)
	}
	if got := (&SongkickClient{}).ua(); got != songkickUserAgent {
		t.Errorf("hand-built client sent %q, want the package default", got)
	}
}
