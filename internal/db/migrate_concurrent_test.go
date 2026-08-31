package db

import (
	"context"
	"sync"
	"testing"
)

// Migrate has to be safe against a second migrator running at the same time.
//
// It was not: the applied-set is read, then each missing file is applied, with
// nothing between the two. Two callers both see a version missing and both
// apply it, and the loser gets "duplicate key value violates unique constraint
// schema_migrations_pkey" -- or, worse, a half-applied DDL failure like
// "duplicate key value violates unique constraint pg_type_typname_nsp_index"
// from two CREATE TYPEs racing.
//
// CI hit exactly this once internal/rate grew a database-backed test: Go runs
// packages in parallel, so two test binaries migrated the same database at
// once. The same race exists for two app instances booting together.
func TestMigrateIsSafeUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	// Deliberately fewer connections than racers. A waiter blocked on the
	// advisory lock holds a pooled connection, so a Migrate that reached back
	// into the pool for its own statements would deadlock here rather than
	// fail -- which is what happened on CI, where pgxpool's default of
	// max(4, NumCPU) is 4 on a 2-core runner and the job simply hung. A dev
	// machine with more cores hides it, so the constraint is pinned.
	pool := testPoolWithMaxConns(t, 2)

	// testPool has already migrated, so start from a clean schema to make the
	// migrations genuinely pending -- otherwise every caller no-ops and the
	// race has nothing to race over.
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	const racers = 4 // > pool max conns, on purpose
	errs := make([]error, racers)
	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait() // release them together
			errs[i] = Migrate(ctx, pool, "../../migrations")
		}()
	}
	start.Done()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("migrator %d: %v", i, err)
		}
	}
}
