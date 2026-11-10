package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SaveConcert marks a concert (by dedup_key) as saved for this user.
// Idempotent. Returns false when the user already holds maxSaved rows and
// this key is not among them.
//
// The ceiling is enforced in the same statement as the insert rather than by
// a count the handler reads first: a save is one POST with a caller-supplied
// key, so a client in a loop would sit inside the window between the two
// round trips and write past the cap indefinitely.
//
// Re-saving something already saved still succeeds at the cap. Refusing it
// would make an idempotent call start failing once the list filled up, which
// the UI reads as "the star didn't stick".
func SaveConcert(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, dedupKey string, maxSaved int) (bool, error) {
	const q = `
WITH inserted AS (
  INSERT INTO user_saved_concerts (user_id, dedup_key, saved_at, artist_ref)
  -- Casts because the parameters first appear inside an INSERT ... SELECT,
  -- where Postgres resolves their types from the SELECT rather than from the
  -- target columns.
  --
  -- artist_ref is copied from the user's newest snapshot holding this key
  -- (migration 0024). A save has to outlive the feed, and the shared concerts
  -- row cannot answer "which artist is this, for this user" -- see
  -- concerts.ApplyArtistRefs. NULL when no snapshot of theirs holds the key,
  -- which reads back as an act with no artist ID rather than borrowing
  -- someone else's.
  SELECT $1::uuid, $2::text, now(), (
    SELECT s.artist_refs -> $2::text
    FROM user_concert_snapshots s
    WHERE s.user_id = $1 AND s.artist_refs ? $2::text
    ORDER BY s.computed_at DESC
    LIMIT 1
  )
  WHERE (SELECT count(*) FROM user_saved_concerts WHERE user_id = $1) < $3::int
  ON CONFLICT (user_id, dedup_key) DO NOTHING
  RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM inserted)
    OR EXISTS (SELECT 1 FROM user_saved_concerts WHERE user_id = $1 AND dedup_key = $2)
`
	var ok bool
	if err := pool.QueryRow(ctx, q, userID, dedupKey, maxSaved).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// UnsaveConcert removes a saved concert. Idempotent — missing rows are not an error.
func UnsaveConcert(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, dedupKey string) error {
	const q = `DELETE FROM user_saved_concerts WHERE user_id = $1 AND dedup_key = $2`
	_, err := pool.Exec(ctx, q, userID, dedupKey)
	return err
}

// SavedConcert is one saved row: the shared concert body plus this user's own
// artist identity for it (migration 0024). ArtistRef is nil for a save made
// when no snapshot of the user's held the key.
type SavedConcert struct {
	Data      []byte
	ArtistRef []byte
}

// GetSavedConcerts returns the concerts this user has saved, ordered by event
// date.
//
// This joins user_saved_concerts straight to the shared concerts table
// rather than reading the user's snapshot. Saving has to outlive the feed:
// a snapshot only holds the current affinity top-200, so an artist slipping
// out of that list on the next recompute used to make the user's saved show
// silently unreachable — the row stayed in the database and vanished from
// the UI with no explanation.
//
// Saves whose concert row has been pruned (the janitor drops events more
// than 7 days past) simply don't come back; the join skips them.
func GetSavedConcerts(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) ([]SavedConcert, error) {
	const q = `
SELECT c.data, s.artist_ref
FROM user_saved_concerts s
JOIN concerts c ON c.dedup_key = s.dedup_key
WHERE s.user_id = $1
ORDER BY c.event_date, c.dedup_key
`
	rows, err := pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedConcert
	for rows.Next() {
		var sc SavedConcert
		if err := rows.Scan(&sc.Data, &sc.ArtistRef); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// GetSavedDedupKeys returns the set of dedup_keys this user has saved. Used
// by the concerts handler to tag each snapshot concert with saved:bool.
func GetSavedDedupKeys(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (map[string]struct{}, error) {
	const q = `SELECT dedup_key FROM user_saved_concerts WHERE user_id = $1`
	rows, err := pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = struct{}{}
	}
	return out, rows.Err()
}
