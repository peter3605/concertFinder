package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockID is the advisory lock every migrator contends for. Arbitrary
// but fixed: advisory locks share one namespace per database, so the only
// requirement is that nothing else in this application picks the same number.
const migrationLockID int64 = 8_675_309

// Migrate applies pending *.up.sql files in dir in order. Idempotent — already
// applied migrations (tracked in schema_migrations) are skipped. Each file is
// applied in its own transaction; on error, that migration is rolled back and
// the caller sees the error.
//
// Filenames must match NNNN_name.up.sql (e.g. 0001_init.up.sql). Down
// migrations are not run by this function — down is a manual operation.
//
// Serialized against other migrators by a session advisory lock. Idempotent is
// not the same as concurrency-safe: the applied-set is read and *then* each
// missing file is applied, so two callers both see a version as pending and
// both run it. The loser gets a duplicate key on schema_migrations, or —
// because DDL races before the bookkeeping does — something far less legible
// like `duplicate key value violates unique constraint pg_type_typname_nsp_index`
// from two CREATE TYPEs. CI hit this the moment a second package grew a
// database-backed test, since Go runs packages in parallel; two app instances
// booting together would race identically.
func Migrate(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	// Held for the whole function, not per statement: the read of the applied
	// set and the applying of what is missing must be one critical section, or
	// the gap between them is exactly the race.
	//
	// Everything below runs on THIS connection rather than the pool. A waiter
	// blocked on the lock is holding a pooled connection while it waits, so a
	// winner that reached back into the pool for its own work would deadlock
	// as soon as the racers outnumbered the pool: pgxpool defaults to
	// max(4, NumCPU), which is 4 on a 2-core CI runner. Using one connection
	// throughout makes the pool size irrelevant.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// Best effort: releasing the connection ends the session and drops the
		// lock anyway. Explicit so a pooled connection is reusable immediately.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.Exec(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	files, err := discoverMigrations(dir)
	if err != nil {
		return err
	}
	for _, m := range files {
		if applied[m.version] {
			continue
		}
		body, err := os.ReadFile(m.path)
		if err != nil {
			return fmt.Errorf("read %s: %w", m.path, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", filepath.Base(m.path), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", filepath.Base(m.path), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		slog.Info("migration applied", "version", m.version, "name", m.name)
	}
	return nil
}

type migration struct {
	version int
	name    string
	path    string
}

var migRe = regexp.MustCompile(`^(\d+)_([^.]+)\.up\.sql$`)

func discoverMigrations(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out = append(out, migration{version: v, name: m[2], path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
