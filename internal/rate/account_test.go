package rate

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

// hideAccountLedger returns a pool whose view of rate_ledger_account is broken
// and whose view of everything else is the real schema, so the account upsert
// -- and nothing else -- fails. There is no seam in the code itself: with a nil
// pool every path short-circuits to unlimited, which is exactly the behaviour
// under test.
//
// The obvious implementation is to rename the table away for the duration of
// the test, and that is what this did first. It is not safe here. `go test
// ./...` gives one database to three package binaries at once -- internal/db
// and internal/jobs migrate and query the same tables -- and CI runs precisely
// that against a fresh Postgres. A rename is a schema change underneath those
// other binaries mid-run, and it failed that way on a cold database:
// intermittently, in a test whose own subject is a database error, which is the
// worst version of a flake to debug.
//
// A search_path shadow is local to this pool. `rate_ledger_account` resolves to
// a stub carrying none of the columns the upsert names; `rate_ledger`, `users`
// and everything else still resolve to public. Nothing another binary can
// observe is altered.
func hideAccountLedger(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	// Named for the package, not the test: schema creation is idempotent and
	// only this package ever puts it on a search_path.
	const schema = "rate_account_failure_shadow"
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
		t.Fatalf("create shadow schema: %v", err)
	}
	// Deliberately missing source/day/count: any statement that reaches this
	// table fails on the column list, before it can do anything.
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS `+schema+`.rate_ledger_account (unusable boolean)`); err != nil {
		t.Fatalf("create shadow table: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	shadow, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("shadow pool: %v", err)
	}
	t.Cleanup(func() {
		shadow.Close()
		if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Errorf("drop shadow schema: %v", err)
		}
	})
	return shadow
}

// Failing fully open here is not the same trade as failing open per user. The
// account ceiling is the one Ticketmaster enforces, so a database blip during
// the nightly fanout would let every concurrent scan spend its whole per-user
// block against a counter nobody is keeping -- and the resulting 403s reach the
// scan looking exactly like artists with no shows.
func TestAccountLedgerWriteFailureGrantsABoundedBlock(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	shadow := hideAccountLedger(t, pool)

	l := &Ledger{Pool: shadow, Caps: Caps{Ticketmaster: 500, TicketmasterAccount: 5000}}
	user := testUser(t, pool)

	r, err := l.Reserve(ctx, user, SourceTicketmaster, 400)
	if err == nil {
		t.Fatal("Reserve reported success though the account charge failed")
	}
	if r.granted != failOpenAccountGrant {
		t.Errorf("granted = %d, want %d — an unbounded grant is what overruns the upstream quota",
			r.granted, failOpenAccountGrant)
	}
	if r.unlimited {
		t.Error("block is unlimited; the account ceiling would not bind at all")
	}
	if r.accountTracked {
		t.Error("block marked account-tracked though nothing landed on the account counter; Release would refund a charge never made")
	}
	// The per-user charge did land. Whatever is not granted must go back to it:
	// charging someone for calls this block can never make drains their own cap
	// for the rest of the day over an outage that was not theirs.
	if got := userCount(t, pool, user, SourceTicketmaster); got != failOpenAccountGrant {
		t.Errorf("user counter = %d, want %d", got, failOpenAccountGrant)
	}
}

// The bound is a ceiling, not a grant. A scan that only wanted a few calls must
// still get only those — handing it failOpenAccountGrant would charge the
// user's ledger for calls nobody asked for.
func TestAccountLedgerWriteFailureNeverGrantsMoreThanAsked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	shadow := hideAccountLedger(t, pool)

	l := &Ledger{Pool: shadow, Caps: Caps{Ticketmaster: 500, TicketmasterAccount: 5000}}
	user := testUser(t, pool)

	r, err := l.Reserve(ctx, user, SourceTicketmaster, 10)
	if err == nil {
		t.Fatal("Reserve reported success though the account charge failed")
	}
	if r.granted != 10 {
		t.Errorf("granted = %d, want 10", r.granted)
	}
	if got := userCount(t, pool, user, SourceTicketmaster); got != 10 {
		t.Errorf("user counter = %d, want 10", got)
	}
}
