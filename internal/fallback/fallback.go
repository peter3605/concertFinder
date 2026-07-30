package fallback

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/bandsintown"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/spotify"
)

// URLResolver finds an artist's official homepage URL by name. Implemented
// by MusicBrainzClient (default, free, ToS-clean) and BraveClient (legacy,
// requires paid-ish API key). Chain accepts either.
type URLResolver interface {
	ResolveOfficialURL(ctx context.Context, artistName string) (string, error)
}

// Chain runs the Tier A → Tier B fallback (§5.4). Any tier returning ≥1
// concert short-circuits later tiers. Errors from one tier are logged but
// don't halt the chain — partial coverage is still coverage.
type Chain struct {
	Pool     *pgxpool.Pool
	Fetcher  *Fetcher
	Resolver URLResolver
	Songkick *SongkickClient
	// VenueGeo geocodes venue city/state when JSON-LD or Songkick omits
	// coordinates. Nil is valid — in that case, no-coord concerts get
	// dropped in the radius filter.
	VenueGeo *VenueGeocoder
	// RateLedger enforces per-user daily caps on Songkick (design §8.3).
	// Nil = no enforcement.
	RateLedger *rate.Ledger
}

// FindEvents returns concerts for an artist that TM+BIT missed. loc is the
// user's location; results are haversine-filtered to it.
func (c *Chain) FindEvents(ctx context.Context, artist spotify.ScoredArtist, loc concerts.Location) []concerts.Concert {
	// Tier A.1 — cached official URL, if any.
	if events := c.tryOfficialSite(ctx, artist, loc, ""); len(events) > 0 {
		return events
	}
	// Tier A.2 — Songkick.
	if c.Songkick != nil {
		if c.RateLedger != nil && !c.RateLedger.AllowFromContext(ctx, rate.SourceSongkick) {
			// Over the per-user Songkick cap; skip and fall through.
		} else {
			evs, err := c.Songkick.SearchArtistEvents(ctx, artist.Name)
			if err != nil && !errors.Is(err, ErrNoAPIKey) {
				slog.Warn("songkick failed", "artist", artist.Name, "err", err)
			}
			out := filterByRadius(ctx, evs, loc, c.VenueGeo)
			for i := range out {
				out[i].Artist.ID = artist.ID
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	// Tier B.1 — resolve an official URL (default MusicBrainz), then Tier B.2
	// scrape the artist's homepage for JSON-LD MusicEvents.
	if c.Resolver != nil {
		officialURL, err := c.Resolver.ResolveOfficialURL(ctx, artist.Name)
		if err != nil && !errors.Is(err, ErrNoAPIKey) {
			slog.Warn("url resolve failed", "artist", artist.Name, "err", err)
		}
		if officialURL != "" {
			_ = db.UpsertArtistResolution(ctx, c.Pool, db.ArtistResolution{
				SpotifyArtistID: artist.ID, OfficialURL: officialURL,
			})
			if events := c.tryOfficialSite(ctx, artist, loc, officialURL); len(events) > 0 {
				return events
			}
		}
	}
	// Terminal fallback (§5.4.2): a prefilled Google search link. Not yielded
	// as a Concert — the frontend can surface this out-of-band if desired.
	return nil
}

func (c *Chain) tryOfficialSite(ctx context.Context, artist spotify.ScoredArtist, loc concerts.Location, override string) []concerts.Concert {
	officialURL := override
	if officialURL == "" {
		res, hit, err := db.GetArtistResolution(ctx, c.Pool, artist.ID)
		if err != nil || !hit {
			return nil
		}
		officialURL = res.OfficialURL
	}
	if officialURL == "" {
		return nil
	}
	base, err := url.Parse(officialURL)
	if err != nil {
		return nil
	}
	base.Path = strings.TrimRight(base.Path, "/")

	var all []concerts.Concert
	for _, p := range ProbeTourPaths {
		u := *base
		u.Path = strings.TrimRight(base.Path, "/") + p
		page, err := c.Fetcher.GetPage(ctx, u.String())
		if err != nil {
			if !errors.Is(err, ErrDisallowed) {
				slog.Debug("fallback fetch failed", "url", u.String(), "err", err)
			}
			continue
		}
		events := ExtractMusicEvents(page, u.String(), artist.Name)
		for _, e := range events {
			e.Artist.ID = artist.ID
			all = append(all, e)
		}
		if len(all) > 0 {
			// Design §5.4.2: homepage or first common path typically covers it.
			break
		}
	}
	return filterByRadius(ctx, all, loc, c.VenueGeo)
}

// filterByRadius drops concerts whose venue lies outside the user's radius.
// Fallback sources (especially official-site JSON-LD for major artists like
// Post Malone) frequently return an entire world tour, so we always need to
// filter. Concerts without coordinates get their city+state geocoded through
// VenueGeo (Nominatim, cached in memory per unique city). If VenueGeo is nil
// or geocoding fails, no-coord concerts are dropped rather than shown.
func filterByRadius(ctx context.Context, cs []concerts.Concert, loc concerts.Location, geo *VenueGeocoder) []concerts.Concert {
	if loc.RadiusMiles <= 0 {
		return cs
	}
	out := cs[:0]
	for _, c := range cs {
		lat, lng := c.Latitude, c.Longitude
		if lat == 0 && lng == 0 && geo != nil {
			if gLat, gLng, ok := geo.Resolve(ctx, c.City, c.State, c.Country); ok {
				lat, lng = gLat, gLng
			}
		}
		if lat == 0 && lng == 0 {
			continue
		}
		if bandsintown.HaversineMiles(loc.Latitude, loc.Longitude, lat, lng) <= float64(loc.RadiusMiles) {
			out = append(out, c)
		}
	}
	return out
}
