package rate

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestCapsLookup(t *testing.T) {
	c := Caps{Ticketmaster: 100, Bandsintown: 200, Songkick: 50}
	if c.Cap(SourceTicketmaster) != 100 {
		t.Errorf("TM cap wrong")
	}
	if c.Cap(SourceBandsintown) != 200 {
		t.Errorf("BIT cap wrong")
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

func TestContextRoundTrip(t *testing.T) {
	id := uuid.New()
	ctx := NewContext(context.Background(), id)
	got, ok := UserFromContext(ctx)
	if !ok || got != id {
		t.Fatalf("ctx round-trip failed: got=%s ok=%v", got, ok)
	}
	if _, ok := UserFromContext(context.Background()); ok {
		t.Fatal("empty ctx should not have user")
	}
}

func TestAllowFromContextNoUserAllows(t *testing.T) {
	l := &Ledger{Caps: Caps{Ticketmaster: 1}}
	if !l.AllowFromContext(context.Background(), SourceTicketmaster) {
		t.Errorf("no user in ctx should allow (test / one-off scripts)")
	}
}
