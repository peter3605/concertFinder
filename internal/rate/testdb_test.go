package rate

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// Database-backed harness, mirroring internal/db/testdb_test.go.
//
// The account ceiling is two SQL upserts and the arithmetic between them, so
// there is no seam a pure unit test could use: with a nil pool every path
// short-circuits to "unlimited", which is precisely the behaviour that must
// NOT be what ships.
//
// Opt-in and skips cleanly, so `go test ./...` on a laptop with no Postgres
// behaves exactly as before.
//
//	docker run -d --rm --name cf-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_USER=test -e POSTGRES_DB=test -p 55433:5432 postgres:16-alpine
//	TEST_DATABASE_URL='postgres://test:test@127.0.0.1:55433/test?sslmode=disable' \
//	  go test ./internal/rate/
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
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
	// Migrations are the schema's source of truth here too.
	if err := db.Migrate(ctx, pool, "../../migrations"); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	// Both ledgers are day-keyed and shared across tests in this package, so
	// each test starts from a clean slate for today.
	if _, err := pool.Exec(ctx, `DELETE FROM rate_ledger_account`); err != nil {
		t.Fatalf("reset account ledger: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM rate_ledger`); err != nil {
		t.Fatalf("reset ledger: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// rate_ledger has a foreign key to users, so a charge needs a real row.
func testUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	const q = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, 'Test User', '\x00', '\x00')
`
	if _, err := pool.Exec(context.Background(), q, id, "spotify-"+id.String()); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func accountCount(t *testing.T, pool *pgxpool.Pool, source Source) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(count), 0) FROM rate_ledger_account WHERE source = $1`,
		string(source)).Scan(&n)
	if err != nil {
		t.Fatalf("read account ledger: %v", err)
	}
	return n
}

func userCount(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, source Source) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(count), 0) FROM rate_ledger WHERE user_id = $1 AND source = $2`,
		id, string(source)).Scan(&n)
	if err != nil {
		t.Fatalf("read user ledger: %v", err)
	}
	return n
}
