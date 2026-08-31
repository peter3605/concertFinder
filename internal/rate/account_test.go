package rate

import (
	"context"
	"testing"
)

func TestAccountCapLookup(t *testing.T) {
	c := Caps{TicketmasterAccount: 5000, SongkickAccount: 4000}
	if got := c.AccountCap(SourceTicketmaster); got != 5000 {
		t.Errorf("TM account cap = %d, want 5000", got)
	}
	if got := c.AccountCap(SourceSongkick); got != 4000 {
		t.Errorf("Songkick account cap = %d, want 4000", got)
	}
	if got := c.AccountCap(Source("unknown")); got != 0 {
		t.Errorf("unknown source = %d, want 0", got)
	}
}

// The whole point of the change: per-user caps multiply, and the upstream
// enforces the total. Two users each well inside their own cap must not be
// able to spend past the account allowance between them.
func TestAccountCeilingBindsAcrossUsers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := &Ledger{Pool: pool, Caps: Caps{Ticketmaster: 100, TicketmasterAccount: 150}}

	first, second := testUser(t, pool), testUser(t, pool)

	r1, err := l.Reserve(ctx, first, SourceTicketmaster, 100)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if r1.granted != 100 {
		t.Fatalf("first granted = %d, want 100 (inside both caps)", r1.granted)
	}

	// The second user is inside their own 100 cap, but only 50 of the shared
	// 150 remain.
	r2, err := l.Reserve(ctx, second, SourceTicketmaster, 100)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if r2.granted != 50 {
		t.Errorf("second granted = %d, want 50 (account allowance remaining)", r2.granted)
	}
	if got := accountCount(t, pool, SourceTicketmaster); got != 150 {
		t.Errorf("account counter = %d, want 150", got)
	}
	// The refused half must not sit on the second user's own counter. Charging
	// them for calls they were never granted would drain their personal cap on
	// days the account was busy -- a second penalty for someone else's usage.
	if got := userCount(t, pool, second, SourceTicketmaster); got != 50 {
		t.Errorf("second user counter = %d, want 50", got)
	}
}

// A capped scan must be able to spend nothing at all rather than overdraw.
func TestAccountCeilingCanGrantNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := &Ledger{Pool: pool, Caps: Caps{Ticketmaster: 100, TicketmasterAccount: 40}}

	first, second := testUser(t, pool), testUser(t, pool)

	if _, err := l.Reserve(ctx, first, SourceTicketmaster, 100); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	r2, err := l.Reserve(ctx, second, SourceTicketmaster, 100)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if r2.granted != 0 {
		t.Errorf("second granted = %d, want 0", r2.granted)
	}
	if r2.Take() {
		t.Error("Take() succeeded on an empty block")
	}
	if !r2.Exhausted() {
		t.Error("Exhausted() = false after a refused Take; the scan would be recorded as complete")
	}
}

// Release must give the remainder back to the shared counter too. Leaving it
// high is the failure that starves every other user for the rest of the day.
func TestReleaseRefundsTheAccountLedger(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := &Ledger{Pool: pool, Caps: Caps{Ticketmaster: 100, TicketmasterAccount: 100}}

	user := testUser(t, pool)
	r, err := l.Reserve(ctx, user, SourceTicketmaster, 100)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !r.TakeN(10) {
		t.Fatal("TakeN(10) refused inside a 100 block")
	}
	if err := r.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}

	if got := accountCount(t, pool, SourceTicketmaster); got != 10 {
		t.Errorf("account counter after release = %d, want 10 (only what was spent)", got)
	}
	if got := userCount(t, pool, user, SourceTicketmaster); got != 10 {
		t.Errorf("user counter after release = %d, want 10", got)
	}
}

// An unset account cap must leave behaviour exactly as it was, so an operator
// who upgrades without setting the new variable is not suddenly throttled.
func TestUnsetAccountCapChangesNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := &Ledger{Pool: pool, Caps: Caps{Ticketmaster: 100}}

	user := testUser(t, pool)
	r, err := l.Reserve(ctx, user, SourceTicketmaster, 100)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if r.granted != 100 {
		t.Errorf("granted = %d, want 100", r.granted)
	}
	if got := accountCount(t, pool, SourceTicketmaster); got != 0 {
		t.Errorf("account counter = %d, want 0 (never charged)", got)
	}
}
