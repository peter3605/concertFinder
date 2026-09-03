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

// ScanCachedConcerts returns raw cached payloads whose cache_key starts with
// prefix and which were fetched within maxAge, newest first and capped at
// limit rows.
//
// GetCachedConcerts answers "what did we last get for this exact artist in
// this exact place"; this answers "what have we got for anywhere lately",
// which is what the signed-out discover view is built from. It is a read of
// data already paid for: nothing about this path can reach an upstream API
// or the rate ledger.
func ScanCachedConcerts(ctx context.Context, pool *pgxpool.Pool, prefix string, maxAge time.Duration, limit int) ([][]byte, error) {
	const q = `
SELECT results FROM concert_cache
WHERE starts_with(cache_key, $1) AND fetched_at > $2
ORDER BY fetched_at DESC
LIMIT $3
`
	rows, err := pool.Query(ctx, q, prefix, time.Now().Add(-maxAge), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		out = append(out, blob)
	}
	return out, rows.Err()
}
