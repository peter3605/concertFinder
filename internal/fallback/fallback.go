package fallback

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/geocoding"
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
}

// isCtxErr reports whether an error is just the scan's fallback deadline
// arriving. Those are expected once the budget is spent and would otherwise
// emit one WARN per remaining artist — a hundred lines saying nothing.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// FindEvents returns concerts for an artist Ticketmaster missed. loc is the
// user's location; results are haversine-filtered to it.
func (c *Chain) FindEvents(ctx context.Context, artist spotify.ScoredArtist, loc concerts.Location) []concerts.Concert {
	// Tier A — Songkick. (The "cached official URL" step from earlier
	// designs collapsed into Tier B once the URL resolver got its own
	// two-tier cache — Tier B is now cache-first-then-fetch by construction.)
	if c.Songkick.Enabled() {
		// Per-user daily quota (design §8.3) rides on the context as a
		// pre-charged reservation taken out by the scan worker.
		if !rate.Allow(ctx, rate.SourceSongkick) {
			// Over the per-user Songkick cap; skip and fall through.
		} else {
			evs, err := c.Songkick.SearchArtistEvents(ctx, artist.Name)
			if err != nil && !errors.Is(err, ErrNoAPIKey) && !isCtxErr(err) {
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
	// Tier B — resolve an official URL (default MusicBrainz, cached in
	// mb_url_cache) then scrape the artist's homepage for JSON-LD MusicEvents.
	if c.Resolver != nil {
		officialURL, err := c.Resolver.ResolveOfficialURL(ctx, artist.Name)
		if err != nil && !errors.Is(err, ErrNoAPIKey) && !isCtxErr(err) {
			slog.Warn("url resolve failed", "artist", artist.Name, "err", err)
		}
		if officialURL != "" {
			if events := c.tryOfficialSite(ctx, artist, loc, officialURL); len(events) > 0 {
				return events
			}
		}
	}
	// Terminal fallback (§5.4.2): a prefilled Google search link. Not yielded
	// as a Concert — the frontend can surface this out-of-band if desired.
	return nil
}

// tryOfficialSite fetches an artist's homepage + a few well-known tour
// paths and extracts JSON-LD MusicEvents. The URL is passed in by the caller
// (from the Resolver on the fallback path); we no longer maintain a
// per-artist-ID URL cache in artist_resolutions — Tier A.1 for the "we
// already know this artist's site" case now hits mb_url_cache via
// c.Resolver instead.
func (c *Chain) tryOfficialSite(ctx context.Context, artist spotify.ScoredArtist, loc concerts.Location, officialURL string) []concerts.Concert {
	if officialURL == "" {
		return nil
	}
	base, err := url.Parse(officialURL)
	if err != nil {
		return nil
	}
	base.Path = strings.TrimRight(base.Path, "/")

	var all []concerts.Concert
	for i, p := range ProbeTourPaths {
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
		// If the homepage carries no JSON-LD *at all*, the site publishes no
		// structured data and the remaining five probe paths are five fetches
		// for nothing. Measured over 91 resolved homepages: 44% had no JSON-LD
		// anywhere, and no artist in that group produced a MusicEvent on any
		// tour path. Skipping them is the difference between 1 and 6 fetches
		// per dead site, each serialized behind the 3s per-host interval and
		// charged to a scan-wide budget.
		//
		// The check is deliberately "any JSON-LD", not "MusicEvent": a site
		// with Organization or WebSite markup is SEO-instrumented and might
		// publish events on a page we haven't fetched yet. One with none
		// won't. See TestJSONLDViability for the measurement.
		if i == 0 && countJSONLDBlocks(page) == 0 {
			slog.Debug("fallback: homepage has no JSON-LD, skipping tour paths",
				"artist", artist.Name, "url", u.String())
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
	// Allocate rather than filter into cs[:0]. Every caller happens to pass
	// a freshly-built slice today, so the in-place form was safe by luck;
	// it would silently truncate any caller that didn't.
	out := make([]concerts.Concert, 0, len(cs))
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
		if geocoding.HaversineMiles(loc.Latitude, loc.Longitude, lat, lng) <= float64(loc.RadiusMiles) {
			out = append(out, c)
		}
	}
	return out
}
