package concerts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// recordingRoundTripper refuses every Ticketmaster request and remembers
// which paths and queries were asked for. The refusal is deliberate: a
// successful empty fetch is cached as a tm: row in concert_cache, and
// internal/db's TestScanCachedConcerts counts every tm: row in the shared test
// database while packages run in parallel. A 400 is not retried and caches
// nothing, and this test only needs to see which request went out.
type recordingRoundTripper struct {
	mu   sync.Mutex
	seen []string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.seen = append(r.seen, req.URL.Path+"?"+req.URL.RawQuery)
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     make(http.Header),
	}, nil
}

// A target that names its attraction is searched by that ID directly: no
// name resolution is spent on it and nothing is written to
// artist_resolutions, which is keyed by profile artist ID and has nothing to
// say about a pick.
func TestSearchOne_KnownAttractionSkipsResolution(t *testing.T) {
	pool := statsTestPool(t)
	rt := &recordingRoundTripper{}
	d := SearchDeps{
		Pool:        pool,
		TM:          ticketmaster.NewClient(&http.Client{Transport: rt}, "test-key"),
		CacheTTL:    time.Hour,
		Parallelism: 1,
	}
	attraction := fmt.Sprintf("K8vTest%d", time.Now().UnixNano())
	target := Target{ID: "tm:" + attraction, Name: "Picked By Hand", AttractionID: attraction}

	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM concert_cache WHERE cache_key LIKE $1`, CachePrefixTicketmaster+target.ID+":%")
	})
	var fs fallbackStats
	if _, err := searchOne(ctx, ctx, d, target, Location{RadiusMiles: 50}, &fs); err != nil {
		t.Fatalf("searchOne: %v", err)
	}

	var sawEvents bool
	for _, s := range rt.seen {
		if strings.Contains(s, "attractions.json") {
			t.Errorf("resolution request made for a target with a known attraction: %s", s)
		}
		if strings.Contains(s, "events.json") && strings.Contains(s, attraction) {
			sawEvents = true
		}
	}
	if !sawEvents {
		t.Errorf("no events request for attraction %s; saw %v", attraction, rt.seen)
	}
	if _, hit, err := db.GetArtistResolution(ctx, pool, target.ID); err != nil {
		t.Fatalf("GetArtistResolution: %v", err)
	} else if hit {
		t.Errorf("artist_resolutions row written for %s", target.ID)
	}
}

// A pick with nothing on Ticketmaster stays empty: the fallback chain is for
// profile artists Ticketmaster does not know, and a pick is by definition one
// it does. The Score is set well above MinFallbackScore so the only thing
// holding the chain shut is the attraction ID.
func TestSearchOne_KnownAttractionNeverEscalates(t *testing.T) {
	fb := &stubFallbacker{out: []Concert{{Artist: ArtistRef{Name: "x"}, Venue: "v", City: "c"}}}
	d := SearchDeps{
		Pool:             statsTestPool(t),
		TM:               ticketmaster.NewClient(&http.Client{Transport: &recordingRoundTripper{}}, "test-key"),
		CacheTTL:         time.Hour,
		Parallelism:      1,
		Fallback:         fb,
		MinFallbackScore: 0,
	}
	attraction := fmt.Sprintf("K8vNoShows%d", time.Now().UnixNano())
	target := Target{ID: "tm:" + attraction, Name: "Between Tours", Score: 99, AttractionID: attraction}

	var fs fallbackStats
	ctx := context.Background()
	out, err := searchOne(ctx, ctx, d, target, Location{RadiusMiles: 50}, &fs)
	if err != nil {
		t.Fatalf("searchOne: %v", err)
	}
	if fb.calls != 0 || len(out) != 0 {
		t.Errorf("fallback chain ran for a pick: calls=%d out=%d", fb.calls, len(out))
	}
	if got := fs.eligible.Load(); got != 0 {
		t.Errorf("eligible = %d, want 0", got)
	}
}
