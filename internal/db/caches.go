package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- MusicBrainz URL cache ---

// GetMBURL returns (url, true, nil) on a cached row (empty string means MB
// was asked and had no homepage), or ("", false, nil) if the artist has
// never been looked up.
func GetMBURL(ctx context.Context, pool *pgxpool.Pool, artistKey string) (string, bool, error) {
	const q = `SELECT official_url FROM mb_url_cache WHERE artist_key = $1`
	var url string
	err := pool.QueryRow(ctx, q, artistKey).Scan(&url)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return url, true, nil
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
