package concerts

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/spotify"
)

// The regression these cover: Search used to swallow every per-artist
// failure and always return a nil error, so a scan that ran out of budget
// after five of two hundred artists was indistinguishable from a complete
// one. The scan worker then stamped the snapshot computed_at = now() and
// the SWR handler sat on those five results for the full staleness window.

func TestSearch_CancelledContextReportsEveryArtistSkipped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	artists := []spotify.ScoredArtist{
		{ID: "a1", Name: "Alpha"},
		{ID: "a2", Name: "Beta"},
		{ID: "a3", Name: "Gamma"},
	}
	// Deps carry a nil pool on purpose: a cancelled context must short
	// out before anything touches the database.
	found, err := Search(ctx, SearchDeps{}, artists, Location{RadiusMiles: 50})
	if len(found) != 0 {
		t.Errorf("expected no results, got %d", len(found))
	}
	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected *IncompleteError, got %v", err)
	}
	if incomplete.SkippedArtists != len(artists) {
		t.Errorf("expected %d skipped, got %d", len(artists), incomplete.SkippedArtists)
	}
	if incomplete.TotalArtists != len(artists) {
		t.Errorf("expected total %d, got %d", len(artists), incomplete.TotalArtists)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("IncompleteError should unwrap to the context cause, got %v", errors.Unwrap(err))
	}
}

func TestSearch_NoArtistsIsComplete(t *testing.T) {
	found, err := Search(context.Background(), SearchDeps{}, nil, Location{})
	if err != nil {
		t.Fatalf("empty input is a complete scan, got %v", err)
	}
	if len(found) != 0 {
		t.Errorf("expected no results, got %d", len(found))
	}
}

func TestSearch_UnusableArtistsDoNotCountAsSkipped(t *testing.T) {
	// Entries missing an ID or name are input noise, not coverage we failed
	// to deliver — they must not make a scan look partial.
	artists := []spotify.ScoredArtist{
		{ID: "", Name: "No ID"},
		{ID: "a1", Name: ""},
	}
	found, err := Search(context.Background(), SearchDeps{}, artists, Location{})
	if err != nil {
		t.Fatalf("expected a complete scan, got %v", err)
	}
	if len(found) != 0 {
		t.Errorf("expected no results, got %d", len(found))
	}
}

func TestNeedsTMResolution(t *testing.T) {
	cases := []struct {
		name string
		r    db.ArtistResolution
		hit  bool
		want bool
	}{
		{
			name: "never looked up",
			hit:  false,
			want: true,
		},
		{
			name: "has an attraction id — never re-resolve",
			r:    db.ArtistResolution{TicketmasterAttractionID: "K8v", ResolvedAt: time.Now().Add(-5 * 365 * 24 * time.Hour)},
			hit:  true,
			want: false,
		},
		{
			name: "fresh negative — trust it",
			r:    db.ArtistResolution{ResolvedAt: time.Now().Add(-24 * time.Hour)},
			hit:  true,
			want: false,
		},
		{
			// The artist may have signed to TM since, or our exact-name
			// match may simply have failed the first time.
			name: "stale negative — ask again",
			r:    db.ArtistResolution{ResolvedAt: time.Now().Add(-NegativeResolutionTTL - time.Hour)},
			hit:  true,
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := needsTMResolution(c.r, c.hit); got != c.want {
				t.Errorf("needsTMResolution = %v, want %v", got, c.want)
			}
		})
	}
}

func TestResolveFallbackBudget(t *testing.T) {
	cases := []struct {
		name       string
		configured time.Duration
		want       time.Duration
	}{
		{"unset falls back to the default", 0, DefaultFallbackBudget},
		{"explicit budget is honored", 30 * time.Second, 30 * time.Second},
		{"negative means disabled", -1, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveFallbackBudget(c.configured); got != c.want {
				t.Errorf("resolveFallbackBudget(%v) = %v, want %v", c.configured, got, c.want)
			}
		})
	}
}

func TestFallbackContext_PositiveBudgetIsLiveThenExpires(t *testing.T) {
	ctx, cancel := fallbackContext(context.Background(), 50*time.Millisecond)
	defer cancel()
	if ctx.Err() != nil {
		t.Fatalf("a positive budget must start live, got %v", ctx.Err())
	}
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", ctx.Err())
	}
}

func TestFallbackContext_NonPositiveBudgetStartsCancelled(t *testing.T) {
	// Disabling the fallback is expressed as an already-dead context so the
	// escalation sites need no special case. Regression: an earlier version
	// handed the *full* scan context to the fallback when the budget was
	// negative — the exact opposite of disabling it.
	for _, budget := range []time.Duration{0 - 1, -time.Minute} {
		ctx, cancel := fallbackContext(context.Background(), budget)
		if ctx.Err() == nil {
			t.Errorf("budget %v must yield an already-cancelled context", budget)
		}
		cancel()
	}
}

func TestFallbackContext_NeverOutlivesParent(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := fallbackContext(parent, time.Hour)
	defer cancel()
	if ctx.Err() != nil {
		t.Fatal("should start live")
	}
	cancelParent()
	<-ctx.Done() // must not hang: fallback ctx is a child of the scan ctx
}

func TestFallbackGate_AdmitsOneAndRefusesTheRest(t *testing.T) {
	g := NewFallbackGate(1)
	ok1, release1 := g.Acquire(context.Background(), 0)
	if !ok1 {
		t.Fatal("first scan should be admitted")
	}
	// A second scan must not be let in to split the shared turnstile.
	ok2, release2 := g.Acquire(context.Background(), 0)
	if ok2 {
		t.Error("second concurrent scan must be refused while the slot is held")
	}
	release2() // safe to call even when refused
	release1()

	// Once released, the next scan gets in.
	ok3, release3 := g.Acquire(context.Background(), 0)
	if !ok3 {
		t.Error("slot should be reusable after release")
	}
	release3()
}

func TestFallbackGate_NilAdmitsEveryone(t *testing.T) {
	// Tests and single-scan deployments shouldn't have to construct one.
	var g *FallbackGate
	for i := 0; i < 3; i++ {
		ok, release := g.Acquire(context.Background(), 0)
		if !ok {
			t.Fatalf("nil gate must admit (attempt %d)", i)
		}
		release()
	}
}

func TestFallbackGate_ZeroCapacityMeansUnlimited(t *testing.T) {
	g := NewFallbackGate(0)
	if g != nil {
		t.Fatal("non-positive capacity should yield a nil (unlimited) gate")
	}
	ok, release := g.Acquire(context.Background(), 0)
	if !ok {
		t.Error("unlimited gate must admit")
	}
	release()
}

func TestFallbackGate_WaitsUpToGraceThenGivesUp(t *testing.T) {
	g := NewFallbackGate(1)
	_, release := g.Acquire(context.Background(), 0)
	defer release()

	start := time.Now()
	ok, r := g.Acquire(context.Background(), 60*time.Millisecond)
	elapsed := time.Since(start)
	r()
	if ok {
		t.Error("should not be admitted while the slot is held")
	}
	if elapsed < 50*time.Millisecond {
		t.Errorf("should have waited out the grace period, gave up after %v", elapsed)
	}
	// The wait must be bounded — burning the caller's whole ScanBudget
	// waiting for a slot is the failure this replaced.
	if elapsed > 2*time.Second {
		t.Errorf("wait was not bounded by the grace period: %v", elapsed)
	}
}

func TestFallbackGate_HandsOffToAWaiter(t *testing.T) {
	g := NewFallbackGate(1)
	_, release := g.Acquire(context.Background(), 0)

	admitted := make(chan bool, 1)
	go func() {
		ok, r := g.Acquire(context.Background(), time.Second)
		defer r()
		admitted <- ok
	}()

	time.Sleep(20 * time.Millisecond)
	release() // holder finishes; the waiter should get in rather than time out

	select {
	case ok := <-admitted:
		if !ok {
			t.Error("a waiter should be admitted once the holder releases")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never observed the release")
	}
}

func TestFallbackGate_AcquireRespectsCancellation(t *testing.T) {
	g := NewFallbackGate(1)
	_, release := g.Acquire(context.Background(), 0)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	ok, r := g.Acquire(ctx, time.Hour) // long grace, but ctx is already dead
	r()
	if ok {
		t.Error("should not be admitted")
	}
	if time.Since(start) > time.Second {
		t.Error("a cancelled scan must not sit in the admission queue")
	}
}

func TestFallbackGate_ReleaseIsIdempotent(t *testing.T) {
	// The release func is deferred on a path that also runs when admission
	// was refused; double-calling must not free someone else's slot.
	g := NewFallbackGate(1)
	ok, release := g.Acquire(context.Background(), 0)
	if !ok {
		t.Fatal("expected admission")
	}
	release()
	release()
	release()

	// Exactly one slot should exist: one acquire succeeds, the next fails.
	okA, relA := g.Acquire(context.Background(), 0)
	okB, relB := g.Acquire(context.Background(), 0)
	relA()
	relB()
	if !okA || okB {
		t.Errorf("repeated release corrupted capacity: okA=%v okB=%v", okA, okB)
	}
}

func TestFallbackGate_ConcurrentContenders(t *testing.T) {
	// The property that matters under real load: no matter how many scans
	// pile in, only `capacity` are inside at any instant.
	g := NewFallbackGate(1)
	var inside, maxInside atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, release := g.Acquire(context.Background(), 500*time.Millisecond)
			if !ok {
				return
			}
			defer release()
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			inside.Add(-1)
		}()
	}
	wg.Wait()
	if got := maxInside.Load(); got > 1 {
		t.Errorf("capacity 1 gate allowed %d concurrent scans into the fallback", got)
	}
}
