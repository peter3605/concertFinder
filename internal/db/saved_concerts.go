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
