package rate

import (
	"context"
	"testing"
)

// A nil ledger and an unset cap both mean "no ceiling here", matching Reserve.
// Neither needs a database, and both are the paths a deployment that never
// configured the account cap actually takes.
func TestReserveAccountWithoutACeiling(t *testing.T) {
	ctx := context.Background()

	var nilLedger *Ledger
	r, err := nilLedger.ReserveAccount(ctx, SourceTicketmaster, 10)
	if err != nil {
		t.Fatalf("nil ledger: %v", err)
	}
	if !r.Take() || !r.unlimited {
		t.Error("a nil ledger must reserve unlimited, as it does for a per-user block")
	}

	l := &Ledger{Caps: Caps{TicketmasterAccount: 0}}
	r, err = l.ReserveAccount(ctx, SourceTicketmaster, 10)
	if err != nil {
		t.Fatalf("unset cap: %v", err)
	}
	if !r.unlimited {
		t.Error("an unset account cap must restore the behaviour from before the ceiling existed")
	}
}

// The seed's spend belongs to no user. rate_ledger.user_id is a foreign key
// into users, so a block that tried to refund a per-user row would be writing
// against a user that does not exist -- Release must touch the account counter
// and nothing else.
func TestReserveAccountReleaseSkipsThePerUserLedger(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := &Ledger{Pool: pool, Caps: Caps{Ticketmaster: 100, TicketmasterAccount: 200}}

	r, err := l.ReserveAccount(ctx, SourceTicketmaster, 80)
	if err != nil {
		t.Fatalf("ReserveAccount: %v", err)
	}
	if r.granted != 80 {
		t.Fatalf("granted = %d, want 80", r.granted)
	}
	if got := accountCount(t, pool, SourceTicketmaster); got != 80 {
		t.Fatalf("account counter = %d, want 80 — the spend must land somewhere", got)
	}

	for i := 0; i < 30; i++ {
		if !r.Take() {
			t.Fatalf("permit %d refused inside the granted block", i)
		}
	}
	// Release must be a plain success: a per-user refund here would fail on
	// the zero UUID rather than quietly doing nothing.
	if err := r.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := accountCount(t, pool, SourceTicketmaster); got != 30 {
		t.Errorf("account counter after release = %d, want 30 (only what was used)", got)
	}
}

// The seed must not be able to spend past the ceiling signed-in users are
// measured against. Past it, Ticketmaster answers 403s that reach a scan
// looking exactly like an artist with no shows.
func TestReserveAccountIsBoundedByTheCeiling(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := &Ledger{Pool: pool, Caps: Caps{Ticketmaster: 100, TicketmasterAccount: 50}}

	first, err := l.ReserveAccount(ctx, SourceTicketmaster, 40)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.granted != 40 {
		t.Fatalf("first granted = %d, want 40", first.granted)
	}

	second, err := l.ReserveAccount(ctx, SourceTicketmaster, 40)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.granted != 10 {
		t.Errorf("second granted = %d, want 10 (what remains of the ceiling)", second.granted)
	}
	if got := accountCount(t, pool, SourceTicketmaster); got != 50 {
		t.Errorf("account counter = %d, want 50 — the overdraw must be handed back", got)
	}
}

// An exhausted block refuses permits rather than silently allowing them. The
// seed reads that refusal as "stop for today", which is the whole reason it
// charges the ledger at all.
func TestReserveAccountRefusesPastItsBlock(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := &Ledger{Pool: pool, Caps: Caps{Ticketmaster: 100, TicketmasterAccount: 3}}

	r, err := l.ReserveAccount(ctx, SourceTicketmaster, 3)
	if err != nil {
		t.Fatalf("ReserveAccount: %v", err)
	}
	for i := 0; i < 3; i++ {
		if !r.Take() {
			t.Fatalf("permit %d refused inside the block", i)
		}
	}
	if r.Take() {
		t.Error("took a fourth permit from a block of three")
	}
	if !r.Exhausted() {
		t.Error("Exhausted() is false after a caller was turned away")
	}
}
