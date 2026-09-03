// Package rate enforces per-user, per-source daily quotas on outbound API
// calls that consume a shared upstream budget. Design §8.3.
//
// Sources of quota pain we care about (in order):
//   - Ticketmaster: 5000 req/day account-wide. One user with 200 unresolved
//     artists on a daily cron can burn through this in under a week of two
//     users.
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
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Source is the enum of quota-tracked upstream services.
type Source string

const (
	SourceTicketmaster Source = "ticketmaster"
	SourceSongkick     Source = "songkick"
)

// AllSources is every quota-tracked source. Keeps callers from having to
// enumerate them by hand when reserving or inspecting a whole scan.
var AllSources = []Source{SourceTicketmaster, SourceSongkick}

// Caps holds the daily upper bounds for each source. Zero disables
// enforcement of that bound.
//
// The two are different questions and both have to be asked. The per-user cap
// stops one heavy account starving the others; the account cap is the number
// the *upstream* actually enforces -- Ticketmaster allows 5000 req/day for the
// API key, regardless of how many of our users are behind it. Without the
// second, per-user caps multiply: ten users at 500/day is 5000, and user
// eleven's scan gets upstream 403s that arrive looking exactly like an artist
// with no shows.
type Caps struct {
	Ticketmaster int
	Songkick     int

	TicketmasterAccount int
	SongkickAccount     int
}

// Cap returns the configured per-user cap for a source, or 0 if unset.
func (c Caps) Cap(s Source) int {
	switch s {
	case SourceTicketmaster:
		return c.Ticketmaster
	case SourceSongkick:
		return c.Songkick
	}
	return 0
}

// AccountCap returns the configured account-wide cap for a source, or 0 if
// unset.
func (c Caps) AccountCap(s Source) int {
	switch s {
	case SourceTicketmaster:
		return c.TicketmasterAccount
	case SourceSongkick:
		return c.SongkickAccount
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
	if newCount > c {
		return false, nil
	}
	// The account ceiling binds here as well. Skipping it would leave a
	// second, unbounded path to the upstream sitting next to the bounded one.
	if acct := l.Caps.AccountCap(source); acct > 0 {
		total, err := l.chargeAccount(ctx, source, 1)
		if err != nil {
			return true, err // fail open
		}
		if total > acct {
			_ = l.refundAccount(ctx, source, 1)
			_ = l.refund(ctx, userID, source, 1)
			return false, nil
		}
	}
	return true, nil
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

// chargeAccount adds n to today's account-wide counter and returns the
// resulting total. Same shape as charge, one row per (source, day).
func (l *Ledger) chargeAccount(ctx context.Context, source Source, n int) (int, error) {
	const q = `
INSERT INTO rate_ledger_account (source, day, count)
VALUES ($1, $2, $3)
ON CONFLICT (source, day) DO UPDATE SET count = rate_ledger_account.count + EXCLUDED.count
RETURNING count
`
	var newCount int
	err := l.Pool.QueryRow(ctx, q, string(source), today(), n).Scan(&newCount)
	return newCount, err
}

// refundAccount returns unspent account quota, clamped at zero for the same
// reason refund is.
func (l *Ledger) refundAccount(ctx context.Context, source Source, n int) error {
	if n <= 0 {
		return nil
	}
	const q = `
UPDATE rate_ledger_account SET count = GREATEST(0, count - $3)
WHERE source = $1 AND day = $2
`
	_, err := l.Pool.Exec(ctx, q, string(source), today(), n)
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
	// accountTracked records that this block was also charged against the
	// account-wide ledger, so Release gives the remainder back to both. A
	// block that skipped the account charge -- because the cap is unset, or
	// because the account upsert errored and we failed open -- must not
	// refund quota it never took.
	accountTracked bool
	granted        int64
	// wanted is what Reserve was asked for, kept only for diagnostics.
	// granted < wanted means the day's cap was already partly spent before
	// this scan began; it does not by itself mean any call was refused.
	// Distinguishing those two is the whole point of reporting it.
	wanted int64
	used   atomic.Int64
	denied atomic.Int64
}

// Take consumes one permit. Returns false once the block is exhausted,
// which callers must treat as "this source is unavailable for this user
// right now" — not as "this source returned no results".
func (r *Reservation) Take() bool { return r.TakeN(1) }

// TakeN consumes n permits at once, for a call site whose single logical
// operation costs more than one upstream request. It is all-or-nothing: a
// partial grant would let the operation start and then fail halfway, which
// spends quota for no result.
//
// Compare-and-swap rather than the add-then-give-back form this started as.
// That version briefly drove `used` past `granted` before correcting, and a
// concurrent caller landing in that window was refused a permit that was
// actually free. Up to 10 artist goroutines per scan reach the fallback at
// once, so the window is real — and a spurious refusal is not a small thing
// here: it sets `denied`, which reads as Exhausted, which stamps the snapshot
// `complete = false` with a `retry_after` of tomorrow midnight. A phantom
// contention loss would cost the user their feed for the rest of the day.
func (r *Reservation) TakeN(n int) bool {
	if r == nil || r.unlimited {
		return true
	}
	if n <= 0 {
		return true
	}
	for {
		cur := r.used.Load()
		if cur+int64(n) > r.granted {
			r.denied.Add(1)
			return false
		}
		if r.used.CompareAndSwap(cur, cur+int64(n)) {
			return true
		}
		// Lost the race; re-read and try again.
	}
}

// Exhausted reports whether any caller was actually turned away.
//
// Deliberately not "used >= granted": a scan of exactly N artists against a
// cap of exactly N spends its whole block while covering every artist, and
// calling that exhausted marks a complete scan incomplete. The SWR handler
// then treats the snapshot as permanently stale and re-enqueues forever —
// observed live with a 200-artist profile against a 200/day cap.
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
	if r.accountTracked {
		// Best effort, and before the per-user refund: leaving the shared
		// counter high is the failure that starves every other user, while
		// leaving one user's counter high costs only them.
		_ = r.ledger.refundAccount(ctx, r.source, int(unused))
	}
	return r.ledger.refund(ctx, r.userID, r.source, int(unused))
}

// failOpenAccountGrant caps what a scan may spend when the account-wide ledger
// cannot be written. Small enough that a handful of concurrent scans riding out
// a database blip stay well inside Ticketmaster's 5000/day, large enough that a
// scan still covers something rather than reporting itself capped and setting a
// retry_after of tomorrow midnight over an outage that lasted seconds.
const failOpenAccountGrant = 50

// Reserve pre-charges up to `want` calls for (user, source) and returns a
// block covering however much of that fits under the cap. A cap of 0, a nil
// ledger, or a failed per-user charge all yield an unlimited block — quota
// accounting is best-effort and must never be the reason a scan fails.
//
// A failed *account* charge is the exception, and grants failOpenAccountGrant:
// that ceiling is the one the upstream enforces, so failing open on it hands
// out quota that does not exist.
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
	if over := want - granted; over > 0 {
		_ = l.refund(ctx, userID, source, over)
	}

	// Now the ceiling the upstream actually enforces. Charged second and only
	// for what the per-user cap already allowed, so the account counter never
	// records more than could possibly be spent.
	accountTracked := false
	if acct := l.Caps.AccountCap(source); acct > 0 && granted > 0 {
		newTotal, err := l.chargeAccount(ctx, source, granted)
		if err != nil {
			// Fail open, but bounded -- unlike the per-user path above. A
			// per-user cap only protects our users from each other, so an
			// unlimited block there costs at most one noisy account. This is
			// the ceiling Ticketmaster itself enforces, and past 5000/day it
			// answers with 403s that reach the scan looking exactly like an
			// artist with no shows -- so a Postgres blip during the nightly
			// fanout would hand every concurrent scan its whole per-user block
			// against a counter nobody is keeping.
			//
			// The block is still NOT marked account-tracked: nothing landed on
			// the account counter, so Release must not refund it. The per-user
			// charge did land, so the part we are declining to grant goes back
			// -- leaving the user charged for calls this block can never make
			// drains their own cap for the rest of the day over an outage that
			// was not theirs.
			bounded := granted
			if bounded > failOpenAccountGrant {
				bounded = failOpenAccountGrant
			}
			if over := granted - bounded; over > 0 {
				_ = l.refund(ctx, userID, source, over)
			}
			slog.Error("rate: account ledger write failed, granting a bounded block",
				"source", string(source), "user", userID,
				"wanted", want, "granted", bounded, "err", err)
			return &Reservation{
				ledger: l, userID: userID, source: source,
				granted: int64(bounded), wanted: int64(want),
			}, err
		}
		accountTracked = true
		allowed := granted - (newTotal - acct)
		if allowed < 0 {
			allowed = 0
		}
		if allowed > granted {
			allowed = granted
		}
		// Hand the overdraw back to BOTH ledgers. Returning it only to the
		// account would leave the user charged for calls they were never
		// granted, and their own cap would drain on days the account was
		// already full -- a second, invisible penalty for someone else's
		// usage.
		if over := granted - allowed; over > 0 {
			_ = l.refundAccount(ctx, source, over)
			_ = l.refund(ctx, userID, source, over)
		}
		granted = allowed
	}

	return &Reservation{
		ledger: l, userID: userID, source: source,
		accountTracked: accountTracked,
		granted:        int64(granted), wanted: int64(want),
	}, nil
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
func (r *Reservations) Take(s Source) bool { return r.TakeN(s, 1) }

// TakeN consumes n permits for a source. See Reservation.TakeN.
func (r *Reservations) TakeN(s Source, n int) bool {
	if r == nil {
		return true
	}
	return r.blocks[s].TakeN(n)
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

// Stat is one source's accounting for a single scan.
type Stat struct {
	Source  Source
	Granted int64
	Used    int64
	Denied  int64
	// Wanted is what the scan asked for. Granted < Wanted means the daily
	// cap was already partly spent when this scan started — which is not the
	// same thing as a call being refused, and the two were indistinguishable
	// before this existed.
	Wanted    int64
	Unlimited bool
}

// Stats reports per-source accounting for the scan.
//
// This exists because "rate_capped: true" is unactionable on its own. It names
// no source, no numbers, and no reason, and a capped scan writes
// complete = false, which the SWR handler reads as permanently stale. Working
// out *why* a scan capped meant reading the ledger table after the fact and
// reconstructing arithmetic across concurrent scans and refunds — by which
// point the reservation, which holds the only copy of `denied`, is gone.
//
// Call before Release: Release zeroes granted and used.
func (r *Reservations) Stats() []Stat {
	if r == nil {
		return nil
	}
	out := make([]Stat, 0, len(AllSources))
	for _, s := range AllSources {
		b := r.blocks[s]
		if b == nil {
			continue
		}
		out = append(out, Stat{
			Source:    s,
			Granted:   b.granted,
			Used:      b.used.Load(),
			Denied:    b.denied.Load(),
			Wanted:    b.wanted,
			Unlimited: b.unlimited,
		})
	}
	return out
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

// AllowN consumes n permits for a source. Use it where one logical lookup
// costs several upstream requests — charging one permit for a two-request
// operation makes the configured cap mean half what it says.
func AllowN(ctx context.Context, s Source, n int) bool {
	return FromContext(ctx).TakeN(s, n)
}
