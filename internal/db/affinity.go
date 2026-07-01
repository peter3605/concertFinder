package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SaveAffinityProfile upserts the JSONB blob for a user. Callers marshal their
// own artist-list type; the db layer stays decoupled from spotify types.
func SaveAffinityProfile(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, artistsJSON []byte) error {
	const q = `
INSERT INTO affinity_profiles (user_id, artists, computed_at)
VALUES ($1, $2, now())
ON CONFLICT (user_id) DO UPDATE SET
  artists     = EXCLUDED.artists,
  computed_at = EXCLUDED.computed_at
`
	_, err := pool.Exec(ctx, q, userID, artistsJSON)
	return err
}

// LoadFreshAffinityProfile returns the blob only if it was computed within ttl.
// On stale/missing, returns (nil, zero, false, nil).
func LoadFreshAffinityProfile(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, ttl time.Duration) ([]byte, time.Time, bool, error) {
	const q = `SELECT artists, computed_at FROM affinity_profiles WHERE user_id = $1 AND computed_at > $2`
	cutoff := time.Now().Add(-ttl)
	var blob []byte
	var computedAt time.Time
	err := pool.QueryRow(ctx, q, userID, cutoff).Scan(&blob, &computedAt)
	if err != nil {
		if err == ErrNoRows {
			return nil, time.Time{}, false, nil
		}
		return nil, time.Time{}, false, err
	}
	return blob, computedAt, true, nil
}
