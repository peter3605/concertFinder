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

	updates := updateStatements(t, "../../migrations/0024_per_user_artist_refs.up.sql", 2)

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

// updateStatements returns the UPDATE statements of a migration file, with
// comments stripped, and fails unless there are exactly want of them -- so a
// statement added to or dropped from the file cannot go untested.
func updateStatements(t *testing.T, path string, want int) []string {
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
	if len(out) != want {
		t.Fatalf("found %d UPDATE statements in %s, want %d", len(out), path, want)
	}
	return out
}

// Migration 0025 is the second half of CF-28: with every reader overlaying
// the viewer's own refs, the shared row's leftover identity is removed. What
// must hold is narrow, and each half of it fails silently if wrong: exactly
// id and genres go (the name is what DedupKey is built from, and a row losing
// its venue or links is a card that renders blank), and rows written since
// 0024 are not rewritten at all.
func TestMigration0025StripsOnlyArtistIdentityFromSharedRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	strip := updateStatements(t, "../../migrations/0025_strip_shared_artist_identity.up.sql", 1)[0]

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE concerts (LIKE public.concerts INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
		t.Fatalf("shadow concerts: %v", err)
	}

	exec(t, tx, `INSERT INTO concerts (dedup_key, data, event_date) VALUES
	  ('legacy',     '{"artist":{"id":"sp1","name":"A","genres":["indie"]},"venue":"V","links":[{"source":"ticketmaster","url":"u"}]}', now()),
	  ('id-only',    '{"artist":{"id":"sp2","name":"B"}}', now()),
	  ('since-0024', '{"artist":{"name":"C"},"venue":"W"}', now()),
	  ('no-artist',  '{"venue":"X"}', now()),
	  ('null-artist','{"artist":null}', now())`)

	tag, err := tx.Exec(ctx, strip)
	if err != nil {
		t.Fatalf("strip failed: %v\n%s", err, strip)
	}
	if got := tag.RowsAffected(); got != 2 {
		t.Errorf("strip rewrote %d rows, want 2 (only the rows still carrying id or genres)", got)
	}

	want := map[string]string{
		"legacy":      `{"links": [{"url": "u", "source": "ticketmaster"}], "venue": "V", "artist": {"name": "A"}}`,
		"id-only":     `{"artist": {"name": "B"}}`,
		"since-0024":  `{"venue": "W", "artist": {"name": "C"}}`,
		"no-artist":   `{"venue": "X"}`,
		"null-artist": `{"artist": null}`,
	}
	for key, w := range want {
		var got string
		if err := tx.QueryRow(ctx, `SELECT data::text FROM concerts WHERE dedup_key = $1`, key).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		if got != w {
			t.Errorf("%s after strip = %s, want %s", key, got, w)
		}
	}

	// A second run is a no-op, so a re-applied or hand-run copy cannot do
	// anything the first did not.
	tag, err = tx.Exec(ctx, strip)
	if err != nil {
		t.Fatalf("second strip: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Errorf("second strip rewrote %d rows, want 0", tag.RowsAffected())
	}
}

func exec(t *testing.T, tx pgx.Tx, q string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		t.Fatalf("exec: %v\n%s", err, q)
	}
}
