package db

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A database-backed test harness.
//
// This repository has deliberately had none until now: every other test is a
// pure unit test, and adding a database to CI is a real cost. It exists here
// because the behaviour in ledger_channel_test.go cannot be tested any other
// way — it is a property of a primary key and two queries, and the failure it
// guards against produces no error, no log, and no observable difference
// except a notification that silently never arrives.
//
// It is opt-in and skips cleanly. Without TEST_DATABASE_URL the package's
// tests still run and pass, so `go test ./...` on a laptop with no Postgres
// behaves exactly as it did before.
//
//	# one-off local run
//	docker run -d --rm --name cf-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_USER=test -e POSTGRES_DB=test -p 55433:5432 postgres:16-alpine
//	TEST_DATABASE_URL='postgres://test:test@127.0.0.1:55433/test?sslmode=disable' \
//	  go test ./internal/db/
//
// Each test gets its own schema so parallel runs cannot see each other's rows,
// and the schema is dropped on cleanup whether the test passed or failed.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testPoolWithMaxConns(t, 0)
}

// testPoolWithMaxConns is testPool with a bounded pool, for the one test that
// needs the bound to be the thing under test. maxConns <= 0 leaves pgxpool's
// default.
func testPoolWithMaxConns(t *testing.T, maxConns int) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
	}
	if maxConns > 0 {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + "pool_max_conns=" + strconv.Itoa(maxConns)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	// Migrations are the schema's source of truth; running them here means
	// this harness cannot drift from production the way a hand-maintained
	// CREATE TABLE fixture would.
	if err := Migrate(ctx, pool, "../../migrations"); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// insertTestUser creates a user row and returns its ID. Every column the
// schema requires is filled; the encrypted token is nonsense bytes because
// nothing under test decrypts it.
func insertTestUser(t *testing.T, pool *pgxpool.Pool, optIn userOptIns) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	const q = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token,
                   refresh_token_nonce, email, digest_opt_in,
                   instant_notify_opt_in, push_opt_in)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
`
	_, err := pool.Exec(ctx, q,
		id, "spotify-"+id.String(), "Test User",
		[]byte("ciphertext"), []byte("nonce-123456"),
		optIn.email, optIn.digest, optIn.instantNotify, optIn.push,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

type userOptIns struct {
	email         string
	digest        bool
	instantNotify bool
	push          bool
}
