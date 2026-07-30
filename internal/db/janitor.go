package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PruneRateLedger deletes counters older than the retention window (days).
func PruneRateLedger(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM rate_ledger WHERE day < (now() - ($1 || ' days')::interval)::date`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

// PruneConcertCache deletes cache entries older than the retention window.
// Note: the runtime cache TTL is shorter (4h read horizon), but keeping rows
// past TTL wastes space; this prunes them properly.
func PruneConcertCache(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM concert_cache WHERE fetched_at < now() - ($1 || ' days')::interval`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

// PruneStaleSnapshots deletes user_concert_snapshots whose location_key no
// longer matches the user's current saved location and haven't been read
// (updated_at proxy) in the given window. Prevents unbounded growth when
// users bounce between cities.
func PruneStaleSnapshots(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	// Delete snapshots for (user, location) that hasn't been the user's
	// current saved location AND hasn't been updated in the window. If a
	// user has no saved location, don't touch their snapshots (fallback
	// location may still be in use).
	const q = `
DELETE FROM user_concert_snapshots s
USING user_locations ul
WHERE s.user_id = ul.user_id
  AND s.location_key <> (
    round(ul.latitude::numeric, 4)::text || ',' ||
    round(ul.longitude::numeric, 4)::text || ',' ||
    ul.radius_miles::text
  )
  AND s.updated_at < now() - ($1 || ' days')::interval
`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

// PruneOldDigestSent trims user_digest_sent rows for concerts long past.
// A concert we sent 6 months ago will never appear in a snapshot again.
func PruneOldDigestSent(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM user_digest_sent WHERE sent_at < now() - ($1 || ' days')::interval`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}
