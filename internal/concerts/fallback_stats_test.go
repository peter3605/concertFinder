package concerts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/spotify"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// These tests pin the distinction that did not exist while a Tier B resolver
// answered 422 to every request for eleven nightly scans: "the chain ran and
// found nothing" and "the chain never ran" produced identical evidence, since
// both are simply artists with no shows. fallbackStats is the only thing that
// tells them apart, so each of its three outcomes gets a test.

func statsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type stubRoundTripper struct{ body string }

func (s stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
	}, nil
}

// tmWithNoAttraction resolves every artist to nothing, which is what puts an
// artist through the escalation gate without any Ticketmaster events.
func tmWithNoAttraction() *ticketmaster.Client {
	return ticketmaster.NewClient(
		&http.Client{Transport: stubRoundTripper{`{"_embedded":{"attractions":[]}}`}},
		"test-key",
	)
}

type stubFallbacker struct {
	calls int
	out   []Concert
}

func (s *stubFallbacker) FindEvents(context.Context, spotify.ScoredArtist, Location) []Concert {
	s.calls++
	return s.out
}

func statsDeps(t *testing.T, fb Fallbacker) SearchDeps {
	return SearchDeps{
		Pool:             statsTestPool(t),
		TM:               tmWithNoAttraction(),
		CacheTTL:         time.Hour,
		Parallelism:      1,
		Fallback:         fb,
		MinFallbackScore: 2.0,
	}
}

func statsArtist(t *testing.T) spotify.ScoredArtist {
	t.Helper()
	return spotify.ScoredArtist{
		ID:    fmt.Sprintf("test-artist-%s-%d", t.Name(), time.Now().UnixNano()),
		Name:  "Nobody Ticketmaster Has Heard Of",
		Score: 3.0,
	}
}

func TestSearchOne_ChainRanAndFoundNothing(t *testing.T) {
	fb := &stubFallbacker{}
	d := statsDeps(t, fb)
	var fs fallbackStats

	ctx := context.Background()
	if _, err := searchOne(ctx, ctx, d, statsArtist(t), Location{RadiusMiles: 50}, &fs); err != nil {
		t.Fatalf("searchOne: %v", err)
	}

	if fb.calls != 1 {
		t.Errorf("chain called %d times, want 1", fb.calls)
	}
	// This is the shape a healthy but unproductive night has. It must be
	// distinguishable from the dead-chain case below, which is the whole point.
	if got := fs.eligible.Load(); got != 1 {
		t.Errorf("eligible = %d, want 1", got)
	}
	if got := fs.attempted.Load(); got != 1 {
		t.Errorf("attempted = %d, want 1", got)
	}
	if got := fs.produced.Load(); got != 0 {
		t.Errorf("produced = %d, want 0", got)
	}
	if got := fs.skipped.Load(); got != 0 {
		t.Errorf("skipped = %d, want 0", got)
	}
}

func TestSearchOne_ChainProducedEvents(t *testing.T) {
	fb := &stubFallbacker{out: []Concert{{Artist: ArtistRef{Name: "x"}, Venue: "v", City: "c"}}}
	d := statsDeps(t, fb)
	var fs fallbackStats

	ctx := context.Background()
	out, err := searchOne(ctx, ctx, d, statsArtist(t), Location{RadiusMiles: 50}, &fs)
	if err != nil {
		t.Fatalf("searchOne: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d concerts, want 1", len(out))
	}
	if got := fs.produced.Load(); got != 1 {
		t.Errorf("produced = %d, want 1", got)
	}
	if got := fs.attempted.Load(); got != 1 {
		t.Errorf("attempted = %d, want 1", got)
	}
}

func TestSearchOne_EligibleButNoBudgetIsNotAnAttempt(t *testing.T) {
	fb := &stubFallbacker{}
	d := statsDeps(t, fb)
	var fs fallbackStats

	ctx := context.Background()
	spent, cancel := context.WithCancel(ctx)
	cancel() // the scan-wide fallback budget is gone; ctx itself is still live

	if _, err := searchOne(ctx, spent, d, statsArtist(t), Location{RadiusMiles: 50}, &fs); err != nil {
		t.Fatalf("searchOne: %v", err)
	}

	if fb.calls != 0 {
		t.Errorf("chain called %d times with no budget, want 0", fb.calls)
	}
	// Eligible still counts: the artist qualified and we declined to spend.
	// Counting only attempts here would make a starved chain and a dead one
	// look the same again, one layer down.
	if got := fs.eligible.Load(); got != 1 {
		t.Errorf("eligible = %d, want 1", got)
	}
	if got := fs.attempted.Load(); got != 0 {
		t.Errorf("attempted = %d, want 0", got)
	}
	if got := fs.skipped.Load(); got != 1 {
		t.Errorf("skipped = %d, want 1", got)
	}
}

func TestSearchOne_BelowMinScoreIsNotEligible(t *testing.T) {
	fb := &stubFallbacker{}
	d := statsDeps(t, fb)
	var fs fallbackStats

	a := statsArtist(t)
	a.Score = 0.5 // under MinFallbackScore

	ctx := context.Background()
	if _, err := searchOne(ctx, ctx, d, a, Location{RadiusMiles: 50}, &fs); err != nil {
		t.Fatalf("searchOne: %v", err)
	}
	if fb.calls != 0 {
		t.Errorf("chain called %d times below min score, want 0", fb.calls)
	}
	if got := fs.eligible.Load(); got != 0 {
		t.Errorf("eligible = %d, want 0", got)
	}
}
