package rate

import "testing"

// Stats exists so an incomplete scan can say *why* it was capped. The two
// failure modes it has to keep apart:
//
//   - granted < wanted: the day's cap was already partly spent when this scan
//     started. No call was refused yet. Fix: scan less often, or raise the cap.
//   - denied > 0:       a caller actually asked for a permit and was turned
//     away. Coverage was lost. Fix: raise the cap.
//
// Collapsing these into one boolean is what made a real capped scan
// undiagnosable from logs alone.

func TestStatsDistinguishShortGrantFromRefusedCall(t *testing.T) {
	// Granted less than wanted, but nobody was ever refused.
	short := &Reservation{granted: 100, wanted: 400}
	if short.Exhausted() {
		t.Fatal("a short grant with no refused call must not report Exhausted")
	}

	rs := &Reservations{blocks: map[Source]*Reservation{SourceTicketmaster: short}}
	got := rs.Stats()
	if len(got) == 0 {
		t.Fatal("expected a stat for ticketmaster")
	}
	s := got[0]
	if s.Granted != 100 || s.Wanted != 400 {
		t.Fatalf("granted/wanted not reported: got granted=%d wanted=%d", s.Granted, s.Wanted)
	}
	if s.Denied != 0 {
		t.Fatalf("nothing was refused, want denied=0, got %d", s.Denied)
	}
}

func TestStatsReportRefusedCalls(t *testing.T) {
	r := &Reservation{granted: 2, wanted: 400}
	if !r.TakeN(2) {
		t.Fatal("first take should fit in the block")
	}
	if r.TakeN(1) {
		t.Fatal("block is spent; this take must be refused")
	}

	rs := &Reservations{blocks: map[Source]*Reservation{SourceTicketmaster: r}}
	s := rs.Stats()[0]
	if s.Used != 2 {
		t.Fatalf("want used=2, got %d", s.Used)
	}
	if s.Denied == 0 {
		t.Fatal("a refused take must show up as denied>0, or the log cannot explain the cap")
	}
	if !rs.AnyExhausted() {
		t.Fatal("a refused take is exactly what Exhausted is meant to report")
	}
}

// Stats must be read before Release in the scan worker. If that order is ever
// reversed the log silently reports zeros, which is worse than the boolean it
// replaced — it looks like real data.
//
// Fully spending the block keeps this off the database: refund short-circuits
// on n <= 0, so Release runs its reset without needing a Pool.
func TestStatsAreZeroedByRelease(t *testing.T) {
	r := &Reservation{ledger: &Ledger{}, granted: 3, wanted: 400}
	if !r.TakeN(3) {
		t.Fatal("block should cover exactly 3")
	}

	rs := &Reservations{blocks: map[Source]*Reservation{SourceTicketmaster: r}}
	if used := rs.Stats()[0].Used; used != 3 {
		t.Fatalf("before Release, want used=3, got %d", used)
	}

	rs.Release(t.Context())

	got := rs.Stats()[0]
	if got.Used != 0 || got.Granted != 0 {
		t.Fatalf("Release resets counters; Stats must be read before it. got used=%d granted=%d",
			got.Used, got.Granted)
	}
	// denied is deliberately preserved across Release so callers can still ask
	// Exhausted() when deciding how to record the scan.
	if got.Denied != 0 {
		t.Fatalf("no take was refused here, want denied=0, got %d", got.Denied)
	}
}
