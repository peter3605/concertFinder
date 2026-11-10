package db

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Migration 0024's backfill is what keeps every existing user's subscribe
// bell lit across the deploy that ships CF-28. The readers stop trusting
// concerts.data for artist identity in that same deploy, so a snapshot or
// save the backfill misses reads back with no artist ID at all: the bell
// renders off, instant-notify never matches, and nothing errors.
//
// The test database is shared and already migrated past 0024, so the
// migration cannot be replayed from the top. Instead the file's own UPDATE
// statements run verbatim inside a transaction against temp-table copies of
// the three tables (temp tables shadow same-named ones on the search path)
// and the transaction is rolled back. Reading the statements out of the file
// is the point: a fixture restating the SQL would pass while the shipped
// migration drifted.
func TestMigration0024BackfillsArtistRefsFromTheSharedRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	updates := backfillStatements(t, "../../migrations/0024_per_user_artist_refs.up.sql")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	for _, table := range []string{"concerts", "user_concert_snapshots", "user_saved_concerts"} {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+table+` (LIKE public.`+table+` INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
			t.Fatalf("shadow %s: %v", table, err)
		}
	}

	user := uuid.New()
	exec(t, tx, `INSERT INTO concerts (dedup_key, data, event_date) VALUES
	  ('k-followed', '{"artist":{"id":"sp1","name":"A","genres":["indie"]}}', now()),
	  ('k-nogenre',  '{"artist":{"id":"sp2","name":"B"}}', now()),
	  ('k-noartist', '{"venue":"somewhere"}', now())`)
	// k-pruned is in the snapshot but its row is gone (the janitor prunes
	// past events); it must simply be absent from the refs.
	exec(t, tx, `INSERT INTO user_concert_snapshots (user_id, location_key, dedup_keys, computed_at, complete)
	  VALUES ($1, 'loc', ARRAY['k-followed','k-nogenre','k-noartist','k-pruned'], now(), true),
	         ($1, 'empty', ARRAY[]::text[], now(), true)`, user)
	exec(t, tx, `INSERT INTO user_saved_concerts (user_id, dedup_key) VALUES ($1, 'k-followed'), ($1, 'k-pruned')`, user)

	for _, q := range updates {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("backfill statement failed: %v\n%s", err, q)
		}
	}

	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT artist_refs FROM user_concert_snapshots WHERE location_key = 'loc'`).Scan(&raw); err != nil {
		t.Fatalf("read refs: %v", err)
	}
	var refs map[string]struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Genres []string `json:"genres"`
	}
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatalf("decode refs %s: %v", raw, err)
	}
	if got := refs["k-followed"]; got.ID != "sp1" || got.Name != "A" || strings.Join(got.Genres, ",") != "indie" {
		t.Errorf("k-followed ref = %+v, want the shared row's artist", got)
	}
	if got := refs["k-nogenre"]; got.ID != "sp2" {
		t.Errorf("k-nogenre ref = %+v, want id sp2", got)
	}
	for _, k := range []string{"k-noartist", "k-pruned"} {
		if _, ok := refs[k]; ok {
			t.Errorf("%s has a ref (%s); there was no artist to copy", k, raw)
		}
	}

	if err := tx.QueryRow(ctx, `SELECT artist_refs FROM user_concert_snapshots WHERE location_key = 'empty'`).Scan(&raw); err != nil {
		t.Fatalf("read empty refs: %v", err)
	}
	if string(raw) != "{}" {
		t.Errorf("empty snapshot refs = %s, want {} (NOT NULL column)", raw)
	}

	var saved []byte
	if err := tx.QueryRow(ctx, `SELECT artist_ref FROM user_saved_concerts WHERE dedup_key = 'k-followed'`).Scan(&saved); err != nil {
		t.Fatalf("read saved ref: %v", err)
	}
	if !strings.Contains(string(saved), `"sp1"`) {
		t.Errorf("saved artist_ref = %s, want the shared row's artist", saved)
	}
	var pruned []byte
	if err := tx.QueryRow(ctx, `SELECT artist_ref FROM user_saved_concerts WHERE dedup_key = 'k-pruned'`).Scan(&pruned); err != nil {
		t.Fatalf("read pruned saved ref: %v", err)
	}
	if pruned != nil {
		t.Errorf("pruned save artist_ref = %s, want NULL", pruned)
	}
}

// backfillStatements returns the UPDATE statements of a migration file, with
// comments stripped.
func backfillStatements(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if i := strings.Index(l, "--"); i >= 0 {
			l = l[:i]
		}
		lines = append(lines, l)
	}
	var out []string
	for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
		stmt = strings.TrimSpace(stmt)
		if strings.HasPrefix(strings.ToUpper(stmt), "UPDATE ") {
			out = append(out, stmt)
		}
	}
	if len(out) != 2 {
		t.Fatalf("found %d UPDATE statements in %s, want 2 (snapshots and saves)", len(out), path)
	}
	return out
}

func exec(t *testing.T, tx pgx.Tx, q string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		t.Fatalf("exec: %v\n%s", err, q)
	}
}
