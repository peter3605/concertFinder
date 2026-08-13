package rate

import (
	"sync"
	"sync/atomic"
	"testing"
)

// A Songkick lookup is two HTTP requests with no cache between them, so
// charging one permit made RATE_CAP_SONGKICK_PER_USER_DAILY mean twice the
// number it says. TakeN is how a multi-request operation pays for itself.
func TestTakeNChargesEveryPermit(t *testing.T) {
	r := &Reservation{granted: 4}
	if !r.TakeN(2) {
		t.Fatal("first 2-permit take should fit in a block of 4")
	}
	if !r.TakeN(2) {
		t.Fatal("second 2-permit take should exhaust the block exactly")
	}
	if r.TakeN(2) {
		t.Error("a block of 4 must not satisfy three 2-permit takes")
	}
	if !r.Exhausted() {
		t.Error("a refused take is lost coverage and must be reported")
	}
}

// All-or-nothing: a partial grant would let the caller start a two-request
// operation and fail halfway, spending quota for no result.
func TestTakeNIsAllOrNothing(t *testing.T) {
	r := &Reservation{granted: 3}
	if !r.TakeN(2) {
		t.Fatal("2 of 3 should be granted")
	}
	if r.TakeN(2) {
		t.Error("only 1 permit remains; a 2-permit take must be refused outright")
	}
	// ...and the refusal must hand the over-draw back, so a cheaper caller
	// can still use what's left rather than finding the block stranded.
	if !r.Take() {
		t.Error("the last single permit was stranded by a refused 2-permit take")
	}
}

func TestTakeNOnUnlimitedAndNil(t *testing.T) {
	var nilRes *Reservation
	if !nilRes.TakeN(5) {
		t.Error("a nil reservation is unlimited")
	}
	if !(&Reservation{unlimited: true}).TakeN(5) {
		t.Error("an unlimited reservation allows any n")
	}
	var nilAll *Reservations
	if !nilAll.TakeN(SourceSongkick, 2) {
		t.Error("nil Reservations allows everything")
	}
}

// Take is TakeN(1); the exact-fit case must still not read as exhausted,
// which is the property the whole "denied, not used >= granted" rule exists
// to protect.
func TestTakeNExactFitIsNotExhausted(t *testing.T) {
	r := &Reservation{granted: 2}
	if !r.TakeN(2) {
		t.Fatal("an exact fit must be granted")
	}
	if r.Exhausted() {
		t.Error("spending the whole block while covering everything is not exhaustion")
	}
}

// Concurrent takes must never refuse a permit that is actually free. The
// add-then-give-back form did: it drove `used` past `granted` before
// correcting, and anyone arriving in that window was denied. A phantom denial
// marks the whole scan rate-capped, which parks the user's feed until the UTC
// day rolls over — an expensive way to lose a race.
func TestTakeNNeverRefusesAFreePermit(t *testing.T) {
	const granted = 200
	for trial := 0; trial < 50; trial++ {
		r := &Reservation{granted: granted}
		var wg sync.WaitGroup
		var granted1, granted2 atomic.Int64
		// 100 two-permit takers and 100 one-permit takers against a block of
		// 200: total demand is 300, so exactly 200 permits' worth must be
		// handed out — no more, and no fewer.
		for i := 0; i < 100; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if r.TakeN(2) {
					granted2.Add(1)
				}
			}()
			go func() {
				defer wg.Done()
				if r.Take() {
					granted1.Add(1)
				}
			}()
		}
		wg.Wait()
		spent := granted2.Load()*2 + granted1.Load()
		if spent > granted {
			t.Fatalf("overspent the block: %d of %d", spent, granted)
		}
		// Anything less than granted-1 means someone was refused a permit that
		// was free (granted-1 is legitimate: only 2-permit takers may remain
		// when one permit is left).
		if spent < granted-1 {
			t.Fatalf("underspent the block: %d of %d — a free permit was refused", spent, granted)
		}
	}
}
