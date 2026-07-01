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
	"github.com/peterho/concertfinder/internal/spotify"
)

// Chain runs the Tier A → Tier B fallback (§5.4). Any tier returning ≥1
// concert short-circuits later tiers. Errors from one tier are logged but
// don't halt the chain — partial coverage is still coverage.
type Chain struct {
	Pool     *pgxpool.Pool
	Fetcher  *Fetcher
	Brave    *BraveClient
	Songkick *SongkickClient
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
		evs, err := c.Songkick.SearchArtistEvents(ctx, artist.Name)
		if err != nil && !errors.Is(err, ErrNoAPIKey) {
			slog.Warn("songkick failed", "artist", artist.Name, "err", err)
		}
		out := filterByRadius(evs, loc)
		for i := range out {
			out[i].Artist.ID = artist.ID
		}
		if len(out) > 0 {
			return out
		}
	}
	// Tier B.1 — Brave Search to discover an official URL, then Tier B.2 scrape.
	if c.Brave != nil {
		officialURL, err := c.Brave.ResolveOfficialURL(ctx, artist.Name)
		if err != nil && !errors.Is(err, ErrNoAPIKey) {
			slog.Warn("brave resolve failed", "artist", artist.Name, "err", err)
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
	return filterByRadius(all, loc)
}

// filterByRadius drops concerts whose venue coordinates lie outside the
// user's radius. Records with no coordinates (JSON-LD often omits geo) are
// kept — the alternative is a permanent false-negative for indie tour pages.
func filterByRadius(cs []concerts.Concert, loc concerts.Location) []concerts.Concert {
	if loc.RadiusMiles <= 0 {
		return cs
	}
	out := cs[:0]
	for _, c := range cs {
		if c.Latitude == 0 && c.Longitude == 0 {
			out = append(out, c)
			continue
		}
		if bandsintown.HaversineMiles(loc.Latitude, loc.Longitude, c.Latitude, c.Longitude) <= float64(loc.RadiusMiles) {
			out = append(out, c)
		}
	}
	return out
}
