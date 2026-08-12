package rate

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestCapsLookup(t *testing.T) {
	c := Caps{Ticketmaster: 100, Songkick: 50}
	if c.Cap(SourceTicketmaster) != 100 {
		t.Errorf("TM cap wrong")
	}
	if c.Cap(SourceSongkick) != 50 {
		t.Errorf("Songkick cap wrong")
	}
	if c.Cap(Source("unknown")) != 0 {
		t.Errorf("unknown source should return 0 cap")
	}
}

func TestCheckAndIncrementDisabledWhenCapZero(t *testing.T) {
	l := &Ledger{Pool: nil, Caps: Caps{}} // all zero => disabled
	// Passing nil pool would panic if we didn't short-circuit; asserting
	// the disabled branch returns true without hitting the DB.
	ok, err := l.CheckAndIncrement(context.Background(), uuid.New(), SourceTicketmaster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Errorf("disabled cap should always allow")
	}
}

func TestReserveWithoutPoolIsUnlimited(t *testing.T) {
	l := &Ledger{Caps: Caps{Ticketmaster: 1}} // cap set, but no pool
	r, err := l.Reserve(context.Background(), uuid.New(), SourceTicketmaster, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < 100; i++ {
		if !r.Take() {
			t.Fatalf("unlimited reservation denied at take %d", i)
		}
	}
	if r.Exhausted() {
		t.Error("unlimited reservation should never report exhausted")
	}
}

func TestReservationHandsOutExactlyGranted(t *testing.T) {
	r := &Reservation{granted: 3}
	for i := 0; i < 3; i++ {
		if !r.Take() {
			t.Fatalf("take %d denied inside the block", i)
		}
	}
	if r.Take() {
		t.Error("take past the block should be denied")
	}
	if !r.Exhausted() {
		t.Error("a denied take should report exhausted")
	}
}

func TestReservationFullySpentButNoDenialIsNotExhausted(t *testing.T) {
	// A scan of exactly N artists against a cap of exactly N spends its
	// whole block while still covering everything. Reporting that as
	// exhausted marked complete scans incomplete, which the SWR handler
	// read as permanently stale — an endless re-enqueue loop, seen live
	// with a 200-artist profile against a 200/day cap.
	r := &Reservation{granted: 200}
	for i := 0; i < 200; i++ {
		if !r.Take() {
			t.Fatalf("take %d denied inside the block", i)
		}
	}
	if r.Exhausted() {
		t.Error("spending the block without turning anyone away is not exhaustion")
	}
}

func TestReservationTakeIsAtomic(t *testing.T) {
	// The whole point of a reservation is that concurrent artist goroutines
	// can spend from it without a lock; make sure exactly `granted` of them
	// win when they all race.
	const granted = 50
	r := &Reservation{granted: granted}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < granted*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.Take() {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != granted {
		t.Errorf("expected exactly %d permits, got %d", granted, allowed)
	}
}

func TestNilReservationIsUnlimited(t *testing.T) {
	var r *Reservation
	if !r.Take() {
		t.Error("nil reservation should allow")
	}
	if r.Exhausted() {
		t.Error("nil reservation should never be exhausted")
	}
	if err := r.Release(context.Background()); err != nil {
		t.Errorf("nil release should be a no-op, got %v", err)
	}
}

func TestContextRoundTrip(t *testing.T) {
	rs := &Reservations{blocks: map[Source]*Reservation{
		SourceTicketmaster: {granted: 1},
	}}
	ctx := NewContext(context.Background(), rs)
	if got := FromContext(ctx); got != rs {
		t.Fatalf("ctx round-trip failed: got %v", got)
	}
	if got := FromContext(context.Background()); got != nil {
		t.Fatal("empty ctx should carry no reservations")
	}
}

func TestAllowWithoutReservationsAllows(t *testing.T) {
	// Unit tests and one-off scripts run without a ledger; they must not be
	// throttled into returning empty results.
	if !Allow(context.Background(), SourceTicketmaster) {
		t.Errorf("no reservations in ctx should allow")
	}
}

func TestReservationsAnyExhausted(t *testing.T) {
	rs := &Reservations{blocks: map[Source]*Reservation{
		SourceTicketmaster: {granted: 1},
		SourceSongkick:     {unlimited: true},
	}}
	if rs.AnyExhausted() {
		t.Fatal("nothing spent yet")
	}
	rs.Take(SourceTicketmaster) // consumes the only permit — still no denial
	if rs.AnyExhausted() {
		t.Error("spending the last permit is not exhaustion")
	}
	rs.Take(SourceTicketmaster) // this one is turned away
	if !rs.AnyExhausted() {
		t.Error("a denied take should surface via AnyExhausted")
	}
	if !rs.Take(SourceSongkick) {
		t.Error("unlimited source should still allow after another source capped")
	}
}
