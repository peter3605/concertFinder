package fallback

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/geocoding"
)

// venueHotCacheSize caps the in-memory hot layer. Same eviction philosophy
// as MusicBrainzClient: an evicted entry means the next hit pays a DB read
// instead of a map read.
const venueHotCacheSize = 5000

// nominatimMinRequestGap honors OSM Nominatim's 1 req/sec/IP policy, with a
// small margin.
const nominatimMinRequestGap = 1100 * time.Millisecond

// VenueGeocoder turns a venue's city/state/country strings into lat/lng via
// Nominatim, with a two-tier cache: in-memory (hot) over Postgres (warm).
// Fallback sources (JSON-LD, Songkick occasionally) sometimes omit geo
// coordinates but almost always include city/state.
//
// Threadsafe. Nominatim's 1 req/sec/IP limit is enforced by a shared rate
// limiter; cache hits bypass the limit. City coordinates are treated as
// immutable (they are, at human timescales), so the cache never expires.
type VenueGeocoder struct {
	G       *geocoding.Client
	Pool    *pgxpool.Pool // optional; enables DB-backed warm cache when non-nil
	cache   *lruCache
	limiter *rateLimiter
}

type cachedGeo struct {
	lat, lng float64
	ok       bool // false = looked up and got nothing; don't re-query
}

func NewVenueGeocoder(g *geocoding.Client) *VenueGeocoder {
	return &VenueGeocoder{
		G:       g,
		cache:   newLRU(venueHotCacheSize),
		limiter: newRateLimiter(nominatimMinRequestGap),
	}
}

// WithPool enables the DB-backed warm cache (venue_geo_cache).
func (v *VenueGeocoder) WithPool(pool *pgxpool.Pool) *VenueGeocoder {
	v.Pool = pool
	return v
}

// Resolve returns (lat, lng, true) if the city was located, or (0, 0, false)
// if geocoding failed or the input was empty. Errors are swallowed — a
// missed lookup just means we drop this concert on the radius check, and the
// caller can't do anything more useful with the error.
func (v *VenueGeocoder) Resolve(ctx context.Context, city, state, country string) (float64, float64, bool) {
	city = strings.TrimSpace(city)
	if city == "" {
		return 0, 0, false
	}
	state = strings.TrimSpace(state)
	country = strings.TrimSpace(country)
	key := strings.ToLower(city) + "|" + strings.ToLower(state) + "|" + strings.ToLower(country)

	if hit, ok := v.cache.Get(key); ok {
		g := hit.(cachedGeo)
		return g.lat, g.lng, g.ok
	}

	// Warm layer.
	if v.Pool != nil {
		if lat, lng, ok, hit, err := db.GetVenueGeo(ctx, v.Pool, key); err == nil && hit {
			v.cache.Set(key, cachedGeo{lat: lat, lng: lng, ok: ok})
			return lat, lng, ok
		}
	}

	query := city
	if state != "" {
		query += ", " + state
	}
	if country != "" {
		query += ", " + country
	}

	if err := v.limiter.Wait(ctx); err != nil {
		return 0, 0, false
	}
	res, err := v.G.Search(ctx, query)
	var got cachedGeo
	if err == nil && res != nil {
		got = cachedGeo{lat: res.Latitude, lng: res.Longitude, ok: true}
	}
	v.cache.Set(key, got)
	if v.Pool != nil {
		_ = db.SaveVenueGeo(ctx, v.Pool, key, got.lat, got.lng, got.ok)
	}
	return got.lat, got.lng, got.ok
}
