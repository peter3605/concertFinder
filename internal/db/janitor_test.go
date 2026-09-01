package db

import (
	"context"
	"sort"
	"testing"
	"time"
)

// artist_resolutions was the one negative cache with no reaper: a row lands for
// every artist anyone's profile has ever contained, and the misses stay forever
// against a 0.5 GB storage cap. The prune's whole risk is the WHERE clause —
// positives are kept on purpose, and re-fetching one costs a resolution call
// per artist on the next scan.
func TestPruneExpiredNegativeResolutionsKeepsPositives(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM artist_resolutions`); err != nil {
		t.Fatalf("reset artist_resolutions: %v", err)
	}
	const ttl = 30 * 24 * time.Hour

	negative := func(id string, age time.Duration) {
		t.Helper()
		const q = `INSERT INTO artist_resolutions (spotify_artist_id, ticketmaster_attraction_id, resolved_at)
		           VALUES ($1, NULL, $2)`
		if _, err := pool.Exec(ctx, q, id, time.Now().Add(-age)); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	positive := func(id, attraction string, age time.Duration) {
		t.Helper()
		const q = `INSERT INTO artist_resolutions (spotify_artist_id, ticketmaster_attraction_id, resolved_at)
		           VALUES ($1, $2, $3)`
		if _, err := pool.Exec(ctx, q, id, attraction, time.Now().Add(-age)); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	negative("stale-miss", ttl+time.Hour)
	negative("fresh-miss", time.Hour)
	// Far older than the TTL, and must survive anyway: an artist who is on
	// Ticketmaster does not stop being on it.
	positive("long-standing-hit", "K8vZ917uc57", 365*24*time.Hour)

	n, err := PruneExpiredNegativeResolutions(ctx, pool, ttl)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}

	rows, err := pool.Query(ctx, `SELECT spotify_artist_id FROM artist_resolutions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var left []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(left)
	if len(left) != 2 || left[0] != "fresh-miss" || left[1] != "long-standing-hit" {
		t.Errorf("remaining = %v, want [fresh-miss long-standing-hit]", left)
	}
}
