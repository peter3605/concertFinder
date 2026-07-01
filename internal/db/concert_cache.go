package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GetCachedConcerts returns the cached blob if fetched_at is within ttl.
// (nil, false, nil) on miss or stale.
func GetCachedConcerts(ctx context.Context, pool *pgxpool.Pool, key string, ttl time.Duration) ([]byte, bool, error) {
	const q = `SELECT results FROM concert_cache WHERE cache_key = $1 AND fetched_at > $2`
	cutoff := time.Now().Add(-ttl)
	var blob []byte
	err := pool.QueryRow(ctx, q, key, cutoff).Scan(&blob)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return blob, true, nil
}

func SaveCachedConcerts(ctx context.Context, pool *pgxpool.Pool, key string, blob []byte) error {
	const q = `
INSERT INTO concert_cache (cache_key, results, fetched_at)
VALUES ($1, $2, now())
ON CONFLICT (cache_key) DO UPDATE SET results = EXCLUDED.results, fetched_at = EXCLUDED.fetched_at
`
	_, err := pool.Exec(ctx, q, key, blob)
	return err
}
