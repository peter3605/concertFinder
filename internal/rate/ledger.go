// Package rate enforces per-user, per-source daily quotas on outbound API
// calls that consume a shared upstream budget. Design §8.3.
//
// Sources of quota pain we care about (in order):
//   - Ticketmaster: 5000 req/day account-wide. One user with 200 unresolved
//     artists on a daily cron can burn through this in under a week of two
//     users.
//   - Bandsintown: personal-use quota is soft but they will start denying
//     traffic if any single client is loud.
//   - Songkick: 5000 req/day per developer key.
//
// Quota is handed out in *reservations* rather than one row-trip per call.
// A scan knows its upper bound up front (one artist costs at most a couple
// of upstream calls per source), so it pre-charges that block in a single
// upsert, hands out permits from an in-process counter, and returns
// whatever it didn't use when the scan ends. That turns what used to be up
// to 400 round trips per scan into two per source, without giving up the
// cross-replica correctness of a DB-backed counter: the block is charged
// before any of it is spent, so a concurrent scan on another replica sees
// the pessimistic number.
package rate

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Source is the enum of quota-tracked upstream services.
type Source string

const (
	SourceTicketmaster Source = "ticketmaster"
	SourceBandsintown  Source = "bandsintown"
	SourceSongkick     Source = "songkick"
)

// AllSources is every quota-tracked source. Keeps callers from having to
// enumerate them by hand when reserving or inspecting a whole scan.
var AllSources = []Source{SourceTicketmaster, SourceBandsintown, SourceSongkick}

// Caps holds the per-user, per-day upper bound for each source. Zero
// disables enforcement for that source.
type Caps struct {
	Ticketmaster int
	Bandsintown  int
	Songkick     int
}

// Cap returns the configured cap for a source, or 0 if unset.
func (c Caps) Cap(s Source) int {
	switch s {
	case SourceTicketmaster:
		return c.Ticketmaster
	case SourceBandsintown:
		return c.Bandsintown
	case SourceSongkick:
		return c.Songkick
	}
	return 0
}

// Ledger is a threadsafe DB-backed rate counter. Concurrent writers for the
// same (user, source) serialize via Postgres row locking rather than
// in-process — safe across replicas.
type Ledger struct {
	Pool *pgxpool.Pool
	Caps Caps
}

// today is the ledger's day bucket. UTC so a ledger day is the same window
// on every replica regardless of host timezone.
func today() string { return time.Now().UTC().Format("2006-01-02") }

// CheckAndIncrement atomically adds 1 to today's counter for (user, source)
// and returns true if the resulting count is within the cap. If the cap is 0
// (disabled) it always returns true and skips the DB write entirely.
//
// Errors are treated as "allow" so a transient DB blip doesn't take down
// concert search. Callers should still log via the returned error.
//
// Prefer Reserve for anything that makes more than a handful of calls.
func (l *Ledger) CheckAndIncrement(ctx context.Context, userID uuid.UUID, source Source) (bool, error) {
	c := l.Caps.Cap(source)
	if c <= 0 {
		return true, nil
	}
	newCount, err := l.charge(ctx, userID, source, 1)
	if err != nil {
		return true, err // fail open
	}
	return newCount <= c, nil
}

// charge adds n to today's counter and returns the resulting total.
func (l *Ledger) charge(ctx context.Context, userID uuid.UUID, source Source, n int) (int, error) {
	const q = `
INSERT INTO rate_ledger (user_id, source, day, count)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, source, day) DO UPDATE SET count = rate_ledger.count + EXCLUDED.count
RETURNING count
`
	var newCount int
	err := l.Pool.QueryRow(ctx, q, userID, string(source), today(), n).Scan(&newCount)
	return newCount, err
}

// refund returns unspent quota. Clamped at zero so a double-refund or a
// day-rollover race can't drive the counter negative.
func (l *Ledger) refund(ctx context.Context, userID uuid.UUID, source Source, n int) error {
	if n <= 0 {
		return nil
	}
	const q = `
UPDATE rate_ledger SET count = GREATEST(0, count - $4)
WHERE user_id = $1 AND source = $2 AND day = $3
`
	_, err := l.Pool.Exec(ctx, q, userID, string(source), today(), n)
	return err
}

// Reservation is a pre-charged block of quota for one (user, source).
// Take() hands out permits from it without touching the DB; whatever is
// left when Release() runs goes back to the ledger.
//
// A nil *Reservation is unlimited, so a missing ledger or an unset cap
// needs no special-casing at the call site.
type Reservation struct {
	ledger    *Ledger
	userID    uuid.UUID
	source    Source
	unlimited bool
	granted   int64
	used      atomic.Int64
	denied    atomic.Int64
}

// Take consumes one permit. Returns false once the block is exhausted,
// which callers must treat as "this source is unavailable for this user
// right now" — not as "this source returned no results".
func (r *Reservation) Take() bool {
	if r == nil || r.unlimited {
		return true
	}
	// Overshoot is impossible: Add returns the post-increment value, so
	// exactly `granted` callers see a value inside the block.
	if r.used.Add(1) <= r.granted {
		return true
	}
	r.denied.Add(1)
	return false
}

// Exhausted reports whether any caller was actually turned away.
//
// Deliberately not "used >= granted": a scan of exactly N artists against a
// cap of exactly N spends its whole block while covering every artist, and
// calling that exhausted marks a complete scan incomplete. The SWR handler
// then treats the snapshot as permanently stale and re-enqueues forever —
// observed live with a 200-artist profile and the 200/day Bandsintown cap.
// What callers actually want to know is whether coverage was lost.
func (r *Reservation) Exhausted() bool {
	if r == nil || r.unlimited {
		return false
	}
	return r.denied.Load() > 0
}

// Release returns unused quota to the ledger. Safe to call more than once;
// subsequent calls are no-ops.
func (r *Reservation) Release(ctx context.Context) error {
	if r == nil || r.unlimited || r.ledger == nil {
		return nil
	}
	used := r.used.Load()
	if used > r.granted {
		used = r.granted
	}
	unused := r.granted - used
	r.granted = 0
	r.used.Store(0)
	// denied is intentionally preserved: callers may inspect Exhausted()
	// after Release when deciding how to record the scan.
	return r.ledger.refund(ctx, r.userID, r.source, int(unused))
}

// Reserve pre-charges up to `want` calls for (user, source) and returns a
// block covering however much of that fits under the cap. A cap of 0, a nil
// ledger, or a DB error all yield an unlimited block — quota accounting is
// best-effort and must never be the reason a scan fails.
func (l *Ledger) Reserve(ctx context.Context, userID uuid.UUID, source Source, want int) (*Reservation, error) {
	if l == nil || l.Pool == nil {
		return &Reservation{unlimited: true}, nil
	}
	capacity := l.Caps.Cap(source)
	if capacity <= 0 {
		return &Reservation{unlimited: true}, nil
	}
	if want < 0 {
		want = 0
	}
	newCount, err := l.charge(ctx, userID, source, want)
	if err != nil {
		return &Reservation{unlimited: true}, err // fail open
	}
	// newCount is the total after charging the whole block. Anything past
	// the cap wasn't ours to spend, so hand it straight back.
	granted := want - (newCount - capacity)
	if granted < 0 {
		granted = 0
	}
	if granted > want {
		granted = want
	}
	r := &Reservation{ledger: l, userID: userID, source: source, granted: int64(granted)}
	if over := want - granted; over > 0 {
		_ = l.refund(ctx, userID, source, over)
	}
	return r, nil
}

// Reservations bundles one block per source for a single unit of work
// (currently: one concert scan). Nil-safe throughout so tests and one-off
// scripts can pass nothing at all.
type Reservations struct {
	blocks map[Source]*Reservation
}

// ReserveAll takes out a block for every source in one go. `want` is the
// per-source upper bound on calls — callers size it from their workload
// (e.g. the number of artists about to be scanned).
func (l *Ledger) ReserveAll(ctx context.Context, userID uuid.UUID, want int) *Reservations {
	rs := &Reservations{blocks: make(map[Source]*Reservation, len(AllSources))}
	for _, s := range AllSources {
		// A Reserve error still yields a usable (unlimited) block; the
		// error itself is not actionable at this layer.
		r, _ := l.Reserve(ctx, userID, s, want)
		rs.blocks[s] = r
	}
	return rs
}

// Take consumes one permit for a source. A nil *Reservations allows
// everything, which is what unit tests and one-off scripts want.
func (r *Reservations) Take(s Source) bool {
	if r == nil {
		return true
	}
	return r.blocks[s].Take()
}

// Exhausted reports whether a source's block ran out.
func (r *Reservations) Exhausted(s Source) bool {
	if r == nil {
		return false
	}
	return r.blocks[s].Exhausted()
}

// AnyExhausted reports whether any source hit its cap. The scan worker uses
// this to mark a snapshot incomplete, since a capped source produces the
// same empty result as a source with genuinely nothing to report.
func (r *Reservations) AnyExhausted() bool {
	if r == nil {
		return false
	}
	for _, s := range AllSources {
		if r.blocks[s].Exhausted() {
			return true
		}
	}
	return false
}

// Release returns every block's unused quota. Call once, via defer.
func (r *Reservations) Release(ctx context.Context) {
	if r == nil {
		return
	}
	for _, b := range r.blocks {
		_ = b.Release(ctx)
	}
}

type reservationsKey struct{}

// NewContext returns a context carrying the scan's quota reservations.
// Called by the scan worker before invoking search functions that spend
// quota (concerts.Search and the fallback chain both read it back out).
func NewContext(ctx context.Context, rs *Reservations) context.Context {
	return context.WithValue(ctx, reservationsKey{}, rs)
}

// FromContext extracts reservations previously stashed by NewContext.
// Returns nil when absent, which every consumer treats as "unlimited".
func FromContext(ctx context.Context) *Reservations {
	rs, _ := ctx.Value(reservationsKey{}).(*Reservations)
	return rs
}

// Allow consumes one permit for a source from the context's reservations.
// The callsite-friendly form used throughout search and the fallback chain.
func Allow(ctx context.Context, s Source) bool {
	return FromContext(ctx).Take(s)
}
