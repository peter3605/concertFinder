package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Interval parameters are passed with make_interval(days => $n) rather than
// the ($n || ' days')::interval string-concatenation idiom this file used to
// use. That idiom makes Postgres infer $n as text, which pgx cannot satisfy
// from a Go int — every one of these prunes failed at encode time with
// "cannot find encode plan". JanitorWorker logs a step failure and
// continues, so the whole retention story had been silently dead.

// PruneRateLedger deletes counters older than the retention window (days).
func PruneRateLedger(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM rate_ledger WHERE day < (now() - make_interval(days => $1))::date`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

// PruneConcertCache deletes cache entries older than the retention window.
// Note: the runtime cache TTL is shorter (4h read horizon), but keeping rows
// past TTL wastes space; this prunes them properly.
func PruneConcertCache(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM concert_cache WHERE fetched_at < now() - make_interval(days => $1)`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

// CurrentLocationKey is a (user_id, location_key) pair naming a snapshot
// that must be preserved because it is the user's live location.
type CurrentLocationKey struct {
	UserID      uuid.UUID
	LocationKey string
}

// PruneStaleSnapshots deletes user_concert_snapshots that are neither the
// user's current saved location nor recently updated. Prevents unbounded
// growth when users bounce between cities.
//
// `keep` is supplied by the caller rather than reconstructed in SQL. An
// earlier version rebuilt the key with round(lat::numeric, 4)::text to
// match Go's strconv.FormatFloat(..., 'f', 4, 64); those agree on nearly
// every input but round half-way cases in opposite directions (Postgres
// half-away-from-zero, Go half-to-even), and a disagreement here silently
// deletes the snapshot the user is actively looking at. One implementation
// of the format, in jobs.LocationKey, is the only safe arrangement.
func PruneStaleSnapshots(ctx context.Context, pool *pgxpool.Pool, keep []CurrentLocationKey, days int) (int64, error) {
	userIDs := make([]uuid.UUID, len(keep))
	locKeys := make([]string, len(keep))
	for i, k := range keep {
		userIDs[i] = k.UserID
		locKeys[i] = k.LocationKey
	}
	// Rows for users with no saved location are left alone entirely — the
	// server-wide fallback location may still be serving them.
	const q = `
DELETE FROM user_concert_snapshots s
WHERE s.updated_at < now() - make_interval(days => $1)
  AND EXISTS (SELECT 1 FROM user_locations ul WHERE ul.user_id = s.user_id)
  AND NOT EXISTS (
    SELECT 1 FROM unnest($2::uuid[], $3::text[]) AS k(user_id, location_key)
    WHERE k.user_id = s.user_id AND k.location_key = s.location_key
  )
`
	tag, err := pool.Exec(ctx, q, days, userIDs, locKeys)
	return tag.RowsAffected(), err
}

// ListCurrentLocations returns every user who has an explicitly saved
// location, with the raw coordinates. The caller turns each into a
// location_key using the same formatter the scan worker writes with.
func ListCurrentLocations(ctx context.Context, pool *pgxpool.Pool) ([]UserLocation, error) {
	const q = `SELECT user_id, latitude, longitude, radius_miles FROM user_locations`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserLocation
	for rows.Next() {
		var l UserLocation
		if err := rows.Scan(&l.UserID, &l.Latitude, &l.Longitude, &l.RadiusMiles); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PruneOldDigestSent trims user_digest_sent rows for concerts long past.
// A concert we sent 6 months ago will never appear in a snapshot again.
func PruneOldDigestSent(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM user_digest_sent WHERE sent_at < now() - make_interval(days => $1)`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

// PrunePastConcerts drops rows in the shared concerts table whose event_date
// is past by the given window. Post-normalization, orphaned concert rows
// still take space even after every user's snapshot has been refreshed
// beyond them.
func PrunePastConcerts(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM concerts WHERE event_date < now() - make_interval(days => $1)`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

// PruneExpiredNegativeMBURLs deletes "MusicBrainz had no homepage" rows past
// NegativeMBURLTTL. Readers already ignore them (GetMBURL applies the cutoff),
// so this is purely about not accumulating rows forever for artists that were
// asked about once and never resolved. Positive rows are kept: they never
// expire, on purpose.
func PruneExpiredNegativeMBURLs(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	const q = `DELETE FROM mb_url_cache WHERE official_url = '' AND resolved_at <= $1`
	tag, err := pool.Exec(ctx, q, time.Now().Add(-NegativeMBURLTTL))
	return tag.RowsAffected(), err
}

// PruneExpiredNegativeGeo is PruneExpiredNegativeMBURLs for venue_geo_cache:
// drops the Nominatim misses whose TTL has passed, keeps every hit.
func PruneExpiredNegativeGeo(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	const q = `DELETE FROM venue_geo_cache WHERE ok = false AND resolved_at <= $1`
	tag, err := pool.Exec(ctx, q, time.Now().Add(-negativeGeoTTL))
	return tag.RowsAffected(), err
}
