// Package testdb gives a test a Postgres database of its own.
//
// Most database-backed tests here share the one database TEST_DATABASE_URL
// names, and packages run in parallel against it. That is cheap and usually
// fine, but some tests read table-wide state: internal/db's
// TestScanCachedConcerts counts every tm: row in concert_cache, so any other
// package that runs a real Ticketmaster fetch at the same moment fails it.
// A test that has to do exactly that -- drive a real scan end to end -- uses
// Isolated instead, and its rows cannot be seen by anyone else's.
//
// This is a non-test package only so that several test packages can share
// it; nothing outside a _test.go file should import it.
package testdb

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/peterho/concertfinder/internal/db"
)

// Isolated creates a fresh database next to TEST_DATABASE_URL's, applies this
// repo's migrations and river's, and drops it when the test ends. It skips,
// as every other database-backed test does, when TEST_DATABASE_URL is unset;
// CI fails the run if that skip ever happens there.
//
// migrationsDir is relative to the calling test's package directory, as with
// db.Migrate.
func Isolated(t testing.TB, migrationsDir string) *pgxpool.Pool {
	t.Helper()
	pool := Empty(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := db.Migrate(ctx, pool, migrationsDir); err != nil {
		t.Fatalf("testdb: migrate: %v", err)
	}
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatalf("testdb: river migrator: %v", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("testdb: river migrate: %v", err)
	}
	return pool
}

// Empty is Isolated without the migrations: a freshly created, uniquely named
// database next to TEST_DATABASE_URL's with nothing in it, dropped when the
// test ends. It exists for a test whose subject is migrating itself, which
// needs every migration genuinely pending and must not get there by dropping
// a schema in the database every other package is using at that moment
// (CF-B24: that DROP SCHEMA deadlocked against a parallel package on CI).
//
// maxConns > 0 bounds the returned pool; <= 0 leaves pgxpool's default.
func Empty(t testing.TB, maxConns int) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("testdb: connect: %v", err)
	}
	name := "cf_iso_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("testdb: create %s: %v", name, err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		if _, err := admin.Exec(dctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("testdb: drop %s: %v", name, err)
		}
		admin.Close()
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("testdb: parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	if maxConns > 0 {
		q := u.Query()
		q.Set("pool_max_conns", strconv.Itoa(maxConns))
		u.RawQuery = q.Encode()
	}
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("testdb: connect %s: %v", name, err)
	}
	// Registered after the drop, so it runs first: the pool must be closed
	// before its database is dropped.
	t.Cleanup(pool.Close)
	return pool
}
