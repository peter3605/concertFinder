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

// Migrate applies pending *.up.sql files in dir in order. Idempotent — already
// applied migrations (tracked in schema_migrations) are skipped. Each file is
// applied in its own transaction; on error, that migration is rolled back and
// the caller sees the error.
//
// Filenames must match NNNN_name.up.sql (e.g. 0001_init.up.sql). Down
// migrations are not run by this function — down is a manual operation.
func Migrate(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	if _, err := pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
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
		tx, err := pool.Begin(ctx)
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
