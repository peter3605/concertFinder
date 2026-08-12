package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- MusicBrainz URL cache ---

// NegativeMBURLTTL is how long a "MusicBrainz has no homepage for this
// artist" record stays trusted. Positive resolutions never expire — an
// artist's official site is stable, and re-fetching it would spend the
// 1 req/sec turnstile for nothing.
//
// Negatives have to expire, for the same reason concerts.NegativeResolutionTTL
// exists on the Ticketmaster side: MusicBrainz is a user-edited database and
// URL relationships are added to it continuously, so "no homepage" is a
// statement about today, not about the artist. Cached permanently it became a
// silent permanent exclusion from the fallback chain — no error, no log, the
// artist just never appears. That matters more now that the fallback is the
// only secondary source.
const NegativeMBURLTTL = 30 * 24 * time.Hour

// GetMBURL returns (url, resolvedAt, true, nil) on a cached row (empty URL
// means MB was asked and had no homepage), or ("", zero, false, nil) if the
// artist has never been looked up or its negative record has aged out.
//
// resolvedAt is returned so the caller's in-memory hot layer can apply the
// same expiry. Without it, promoting a negative into that cache would restart
// its clock and the row could outlive NegativeMBURLTTL by up to another full
// TTL.
func GetMBURL(ctx context.Context, pool *pgxpool.Pool, artistKey string) (string, time.Time, bool, error) {
	// Positive rows always count as a hit regardless of age; negatives
	// expire. Mirrors GetVenueGeo — the cutoff is computed here rather than
	// assembled from string fragments in SQL, which is what broke the
	// janitor's interval predicates.
	const q = `
SELECT official_url, resolved_at
FROM mb_url_cache
WHERE artist_key = $1
  AND (official_url <> '' OR resolved_at > $2)
`
	var url string
	var resolvedAt time.Time
	err := pool.QueryRow(ctx, q, artistKey, time.Now().Add(-NegativeMBURLTTL)).Scan(&url, &resolvedAt)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return "", time.Time{}, false, nil
		}
		return "", time.Time{}, false, err
	}
	return url, resolvedAt, true, nil
}

// SaveMBURL upserts. Empty URL is a valid record — the "MB tried and found
// nothing" negative cache entry.
func SaveMBURL(ctx context.Context, pool *pgxpool.Pool, artistKey, url string) error {
	const q = `
INSERT INTO mb_url_cache (artist_key, official_url, resolved_at)
VALUES ($1, $2, now())
ON CONFLICT (artist_key) DO UPDATE SET
  official_url = EXCLUDED.official_url,
  resolved_at  = EXCLUDED.resolved_at
`
	_, err := pool.Exec(ctx, q, artistKey, url)
	return err
}

// --- Venue geocode cache ---

// negativeGeoTTL is how long we trust a "Nominatim returned nothing" record
// before re-asking. Positive matches never expire (city coordinates don't
// drift), but negative results might be due to transient Nominatim data
// issues that get better over time.
const negativeGeoTTL = 30 * 24 * time.Hour

// GetVenueGeo returns cached (lat, lng, ok, cacheHit, err). ok=false means
// Nominatim was asked and returned nothing (negative cache). Negative
// entries older than negativeGeoTTL are treated as misses so we re-query.
func GetVenueGeo(ctx context.Context, pool *pgxpool.Pool, placeKey string) (float64, float64, bool, bool, error) {
	// Positive rows always count as a hit regardless of age; negatives
	// expire. The cutoff is computed here rather than assembled from string
	// fragments in SQL.
	const q = `
SELECT latitude, longitude, ok
FROM venue_geo_cache
WHERE place_key = $1
  AND (ok = true OR resolved_at > $2)
`
	var lat, lng float64
	var ok bool
	err := pool.QueryRow(ctx, q, placeKey, time.Now().Add(-negativeGeoTTL)).Scan(&lat, &lng, &ok)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return 0, 0, false, false, nil
		}
		return 0, 0, false, false, err
	}
	return lat, lng, ok, true, nil
}

// SaveVenueGeo upserts a geocode result (positive or negative).
func SaveVenueGeo(ctx context.Context, pool *pgxpool.Pool, placeKey string, lat, lng float64, ok bool) error {
	const q = `
INSERT INTO venue_geo_cache (place_key, latitude, longitude, ok, resolved_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (place_key) DO UPDATE SET
  latitude    = EXCLUDED.latitude,
  longitude   = EXCLUDED.longitude,
  ok          = EXCLUDED.ok,
  resolved_at = EXCLUDED.resolved_at
`
	_, err := pool.Exec(ctx, q, placeKey, lat, lng, ok)
	return err
}
