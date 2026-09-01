package ticketmaster

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// rewriteTransport aims the client at an httptest server without adding a
// base-URL field to the production Client. APIBase is a package constant, so
// ResolveAttraction and SearchEvents compose absolute app.ticketmaster.com
// URLs; rewriting only the scheme and host leaves the path and query byte-for-
// byte as the client built them, which is exactly what the two-stage
// resolution assertions need to read back.
type rewriteTransport struct {
	base *url.URL
	next http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = rt.base.Scheme
	r.URL.Host = rt.base.Host
	r.Host = "" // fall back to r.URL.Host rather than app.ticketmaster.com
	return rt.next.RoundTrip(r)
}

// apiRecorder stands in for the Discovery API and remembers every request it
// was asked for. Half of what these tests check is not the decoded result but
// the request the client chose to make -- whether resolution went through
// /attractions.json before /events.json, and whether the events query carried
// an attractionId rather than a keyword.
type apiRecorder struct {
	mu   sync.Mutex
	reqs []url.URL
}

func (r *apiRecorder) record(u *url.URL) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, *u)
}

func (r *apiRecorder) requests() []url.URL {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]url.URL(nil), r.reqs...)
}

func (r *apiRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// newTestAPI wires a Client to a recording test server. The handler decides
// what to reply with; the recorder captures what was asked.
func newTestAPI(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) (*Client, *apiRecorder) {
	t.Helper()
	rec := &apiRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.URL)
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	c := NewClient(&http.Client{
		Timeout:   5 * time.Second,
		Transport: rewriteTransport{base: base, next: http.DefaultTransport},
	}, secret)
	return c, rec
}

// serveFixture replies with one testdata file for every request.
func serveFixture(t *testing.T, name string) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	body := fixture(t, name)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// fixture reads a recorded-shape Discovery API payload. Fixtures live in
// testdata/ so the bodies stay valid JSON that a reader can diff against a
// real response; go test excludes the directory from compilation.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// assertQuery checks one query-string parameter the client composed.
func assertQuery(t *testing.T, q url.Values, key, want string) {
	t.Helper()
	if got := q.Get(key); got != want {
		t.Errorf("query %s = %q, want %q", key, got, want)
	}
}

// eventsByID indexes a decode result so a table test can name the event it
// means instead of depending on slice positions, which shift whenever a
// fixture gains a case.
func eventsByID(evs []Event) map[string]Event {
	m := make(map[string]Event, len(evs))
	for _, e := range evs {
		m[e.ID] = e
	}
	return m
}

// testLocation is Washington DC to 4 decimal places, which is the precision
// SearchEvents formats latlong at -- picking a value that survives the
// rounding keeps the query-string assertion about the client's choices
// rather than about float formatting.
const (
	testLat    = 38.9172
	testLng    = -77.0369
	testRadius = 50
)
