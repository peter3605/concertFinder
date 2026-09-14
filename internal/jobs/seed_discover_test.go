package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// rewriteTransport aims the Ticketmaster client at a test server. APIBase is a
// package constant, so the client composes absolute app.ticketmaster.com URLs
// and only the scheme and host are replaced — the path and query stay exactly
// as the client built them.
type rewriteTransport struct {
	base *url.URL
	next http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = rt.base.Scheme
	r.URL.Host = rt.base.Host
	r.Host = ""
	return rt.next.RoundTrip(r)
}

// writtenRow is one cache write the worker attempted.
type writtenRow struct {
	key  string
	blob []byte
}

type seedRecorder struct {
	mu   sync.Mutex
	rows []writtenRow
}

func (r *seedRecorder) save(_ context.Context, key string, blob []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, writtenRow{key: key, blob: blob})
	return nil
}

func (r *seedRecorder) written() []writtenRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]writtenRow(nil), r.rows...)
}

// newSeedWorker builds a worker whose Ticketmaster client talks to h and whose
// cache writes are recorded rather than performed. Ledger is nil throughout:
// a nil ledger reserves unlimited, which is the same short-circuit every other
// quota-free test in the tree relies on, and keeps these tests about the
// worker rather than about Postgres.
func newSeedWorker(t *testing.T, cities []config.SeedCity, h http.HandlerFunc) (*SeedDiscoverWorker, *seedRecorder) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	rec := &seedRecorder{}
	return &SeedDiscoverWorker{
		TM: ticketmaster.NewClient(&http.Client{
			Timeout:   5 * time.Second,
			Transport: rewriteTransport{base: base, next: http.DefaultTransport},
		}, "test-key"),
		Cities: cities,
		save:   rec.save,
	}, rec
}

// onePage replies with a single complete page holding one event.
func onePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"_embedded":{"events":[{"id":"e1","name":"Show","dates":{"start":{"localDate":"2099-06-01"}},"_embedded":{"venues":[{"name":"Room","city":{"name":"Anywhere"},"location":{"latitude":"40.71","longitude":"-74.00"}}]}}]},"page":{"size":100,"totalPages":1,"number":0}}`)
}

var testCities = []config.SeedCity{
	{Name: "New York", Latitude: 40.7128, Longitude: -74.0060},
	{Name: "Chicago", Latitude: 41.8781, Longitude: -87.6298},
}

func TestSeedDiscoverWritesOneRowPerCityUnderTheDiscoverPrefix(t *testing.T) {
	w, rec := newSeedWorker(t, testCities, onePage)

	if err := w.Work(context.Background(), &river.Job[SeedDiscoverArgs]{}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	rows := rec.written()
	if len(rows) != len(testCities) {
		t.Fatalf("wrote %d rows, want %d (one per city)", len(rows), len(testCities))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !strings.HasPrefix(row.key, concerts.CachePrefixTicketmaster) {
			t.Errorf("key %q is outside %q, so the discover view will never read it",
				row.key, concerts.CachePrefixTicketmaster)
		}
		if seen[row.key] {
			t.Errorf("two cities wrote the same key %q", row.key)
		}
		seen[row.key] = true

		// The payload has to be what FromCachedTicketmaster expects; a shape
		// only this worker understands would decode to nothing, silently.
		var evs []ticketmaster.Event
		if err := json.Unmarshal(row.blob, &evs); err != nil {
			t.Errorf("payload for %q does not decode as []ticketmaster.Event: %v", row.key, err)
		}
		if len(evs) == 0 {
			t.Errorf("payload for %q is empty", row.key)
		}
	}

	// And each key is the one the view's own helper composes for that city.
	for _, city := range testCities {
		want := concerts.DiscoverSeedCacheKey(concerts.DiscoverSeedLocation(city.Latitude, city.Longitude))
		if !seen[want] {
			t.Errorf("no row written for %s under %q", city.Name, want)
		}
	}
}

// The rule loadOrFetchTM follows for the same reason: a truncated listing
// written here is served for the life of the row, and no later run can
// correct it while the row is still answering.
//
// Truncation without an error is the case that matters, because that is the
// one a caller can mistake for a complete answer. A market deep enough to hit
// ticketmaster.MaxEventPages produces exactly that: every request succeeds and
// the result is still short.
func TestSeedDiscoverDoesNotCacheATruncatedCity(t *testing.T) {
	var mu sync.Mutex
	var requests int
	w, rec := newSeedWorker(t, testCities[:1], func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "0"
		}
		rw.Header().Set("Content-Type", "application/json")
		// Always another page to fetch, so the client stops at its own limit
		// rather than at the end of the listing.
		fmt.Fprintf(rw, `{"_embedded":{"events":[{"id":"e%s","name":"Show","dates":{"start":{"localDate":"2099-06-01"}},"_embedded":{"venues":[{"name":"Room","city":{"name":"NY"},"location":{"latitude":"40.71","longitude":"-74.00"}}]}}]},"page":{"size":100,"totalPages":99,"number":%s}}`, page, page)
	})

	if err := w.Work(context.Background(), &river.Job[SeedDiscoverArgs]{}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if requests != ticketmaster.MaxEventPages {
		t.Errorf("made %d requests, want %d (the deep-paging limit)", requests, ticketmaster.MaxEventPages)
	}
	if rows := rec.written(); len(rows) != 0 {
		t.Fatalf("wrote %d rows for a truncated fetch, want 0 — a short listing cached here cannot be corrected", len(rows))
	}
}

// One market that fails must not take the others with it. It keeps whatever it
// already had until the janitor's horizon, and tomorrow's run tries again.
func TestSeedDiscoverContinuesPastAFailingCity(t *testing.T) {
	var mu sync.Mutex
	var n int
	// 404 rather than a 5xx: the client retries 5xx with backoff, so a
	// transient-looking failure would simply succeed on the second attempt and
	// this test would be asserting nothing.
	w, rec := newSeedWorker(t, testCities, func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		onePage(rw, r)
	})

	if err := w.Work(context.Background(), &river.Job[SeedDiscoverArgs]{}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if rows := rec.written(); len(rows) != 1 {
		t.Fatalf("wrote %d rows, want 1 — the second city should have been seeded anyway", len(rows))
	}
}

// Configured off must be a quiet no-op rather than an error: an operator who
// sets DISCOVER_SEED_CITIES=none has said the landing page may be empty.
func TestSeedDiscoverWithNoCitiesDoesNothing(t *testing.T) {
	w, rec := newSeedWorker(t, nil, func(http.ResponseWriter, *http.Request) {
		t.Error("no city configured, but the worker called Ticketmaster")
	})

	if err := w.Work(context.Background(), &river.Job[SeedDiscoverArgs]{}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if rows := rec.written(); len(rows) != 0 {
		t.Fatalf("wrote %d rows with no cities configured", len(rows))
	}
}

// The seed's rows have to stay inside webhttp.DiscoverCacheMaxAge and the
// janitor's concert_cache prune, both 7 days. A daily schedule has six days of
// slack; a schedule that ever became weekly would have none, and the symptom
// would be a landing page that empties out rather than an error.
func TestSeedDiscoverRefreshesWellInsideThePruneHorizon(t *testing.T) {
	const pruneHorizon = 7 * 24 * time.Hour
	if SeedDiscoverBudget >= pruneHorizon {
		t.Fatalf("one run may take %v, which is not meaningfully inside the %v horizon", SeedDiscoverBudget, pruneHorizon)
	}
}
