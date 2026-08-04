package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SaveConcert marks a concert (by dedup_key) as saved for this user. Idempotent.
func SaveConcert(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, dedupKey string) error {
	const q = `
INSERT INTO user_saved_concerts (user_id, dedup_key, saved_at)
VALUES ($1, $2, now())
ON CONFLICT (user_id, dedup_key) DO NOTHING
`
	_, err := pool.Exec(ctx, q, userID, dedupKey)
	return err
}

// UnsaveConcert removes a saved concert. Idempotent — missing rows are not an error.
func UnsaveConcert(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, dedupKey string) error {
	const q = `DELETE FROM user_saved_concerts WHERE user_id = $1 AND dedup_key = $2`
	_, err := pool.Exec(ctx, q, userID, dedupKey)
	return err
}

// GetSavedConcerts returns the raw concert bodies this user has saved,
// ordered by event date.
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
func GetSavedConcerts(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) ([][]byte, error) {
	const q = `
SELECT c.data
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
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
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
