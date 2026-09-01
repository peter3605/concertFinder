package db

import (
	"context"
	"fmt"
	"testing"
)

// The three capped inserts are hand-written CTEs whose parameters first appear
// inside an INSERT ... SELECT, which is a shape Postgres will refuse outright
// if the casts are wrong. None of that is visible to the compiler, and the
// caps themselves are the kind of arithmetic that is easy to get off by one in
// the direction that never refuses anything.

func TestSaveConcertRefusesPastTheCeilingButStaysIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "save@example.com"})

	const cap = 3
	for i := 0; i < cap; i++ {
		ok, err := SaveConcert(ctx, pool, user, fmt.Sprintf("key-%d", i), cap)
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("save %d refused inside the ceiling", i)
		}
	}
	// One past the ceiling.
	ok, err := SaveConcert(ctx, pool, user, "key-overflow", cap)
	if err != nil {
		t.Fatalf("overflow save: %v", err)
	}
	if ok {
		t.Error("a save past the ceiling reported success")
	}
	// Re-saving something already saved must keep working at the cap, or the
	// star button starts failing on shows the user already starred.
	ok, err = SaveConcert(ctx, pool, user, "key-0", cap)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if !ok {
		t.Error("re-saving an existing row failed once the list was full")
	}
	if n := countForUser(t, pool, "user_saved_concerts", user); n != cap {
		t.Errorf("saved rows = %d, want %d", n, cap)
	}
}

func TestSubscribeArtistRefusesPastTheCeilingButStaysIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "sub@example.com"})

	const cap = 2
	for i := 0; i < cap; i++ {
		ok, err := SubscribeArtist(ctx, pool, user, fmt.Sprintf("artist-%d", i), "Name", cap)
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("subscribe %d refused inside the ceiling", i)
		}
	}
	if ok, err := SubscribeArtist(ctx, pool, user, "artist-overflow", "Name", cap); err != nil {
		t.Fatalf("overflow subscribe: %v", err)
	} else if ok {
		t.Error("a subscription past the ceiling reported success")
	}
	// Renaming an existing subscription at the cap must still succeed.
	if ok, err := SubscribeArtist(ctx, pool, user, "artist-0", "Renamed", cap); err != nil {
		t.Fatalf("re-subscribe: %v", err)
	} else if !ok {
		t.Error("re-subscribing an existing artist failed once the list was full")
	}
}

// The churn bound is set membership, not a count. That distinction is the
// whole reason this is its own table: a counter would charge a commuter twice
// every morning and lock them out by lunchtime.
func TestLocationVisitsBoundDistinctLocationsNotRequests(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "loc@example.com"})

	const cap = 2
	for i := 0; i < cap; i++ {
		ok, err := RecordLocationVisit(ctx, pool, user, fmt.Sprintf("loc-%d", i), cap)
		if err != nil {
			t.Fatalf("visit %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("visit %d refused inside the cap", i)
		}
	}
	// Returning to a location already opened today must be free, however many
	// times it happens.
	for i := 0; i < 5; i++ {
		ok, err := RecordLocationVisit(ctx, pool, user, "loc-0", cap)
		if err != nil {
			t.Fatalf("revisit: %v", err)
		}
		if !ok {
			t.Fatal("revisiting today's own location was refused; a commuter toggling home/work would be locked out")
		}
	}
	// A genuinely new one past the cap is refused.
	if ok, err := RecordLocationVisit(ctx, pool, user, "loc-new", cap); err != nil {
		t.Fatalf("overflow visit: %v", err)
	} else if ok {
		t.Error("a third distinct location was allowed under a cap of 2")
	}
	// Another user is unaffected — the bound is per account.
	other := insertTestUser(t, pool, userOptIns{email: "loc2@example.com"})
	if ok, err := RecordLocationVisit(ctx, pool, other, "loc-new", cap); err != nil {
		t.Fatalf("other user visit: %v", err)
	} else if !ok {
		t.Error("one user's churn bound refused another user")
	}
}
