package concerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// GenreTarget is one genre a user picked: a top-level Ticketmaster Music
// genre (CF-31, docs/public-path.md §1.2 and §5).
type GenreTarget struct {
	ID   string
	Name string
}

// GenreGridDegrees is the grid a genre listing's location snaps to. Cache
// keys carry coordinates to four decimals, so without snapping two users
// share a listing only when their coordinates are identical -- which a city
// typed into the location box gives and a device fix never does
// (docs/public-path.md §2.1). On a 0.25° grid, everyone in the same cell with
// the same radius costs one fetch between them.
const GenreGridDegrees = 0.25

// GenreFetchPadMiles widens a snapped listing's radius so it still covers the
// user's own circle. The farthest a point can be from its cell's centre is
// half the cell's diagonal: about 12 miles on a 0.25° grid at the equator,
// less everywhere in the US, where a degree of longitude is shorter.
const GenreFetchPadMiles = 15

// MaxGenreFeedConcerts caps the genre part of one snapshot, across all of a
// user's genres. One listing is already at most ticketmaster.MaxGenrePages
// pages (300 events), but five genres in a dense city is 1,500, and the feed,
// the facets and the digest all read the snapshot whole
// (docs/public-path.md §2.4).
const MaxGenreFeedConcerts = 300

// TicketmasterArtistID is the viewer-facing artist ID for a Ticketmaster
// attraction: the identity a pick's snapshot records and the bell
// subscribes to. A genre listing's acts use it too, so a show found through a
// genre can be followed exactly like one found through a pick.
func TicketmasterArtistID(attractionID string) string { return "tm:" + attractionID }

// GenreCell snaps loc to the genre grid and pads its radius. The result is
// what is fetched and what the cache is keyed on; the user's own loc is what
// the fetched events are then filtered to, with Near, as /api/discover does.
func GenreCell(loc Location) Location {
	snap := func(v float64) float64 {
		s := math.Round(v/GenreGridDegrees) * GenreGridDegrees
		if s == 0 {
			return 0 // no "-0.0000" in a cache key
		}
		return s
	}
	return Location{
		Latitude:    snap(loc.Latitude),
		Longitude:   snap(loc.Longitude),
		RadiusMiles: loc.RadiusMiles + GenreFetchPadMiles,
	}
}

// GenreCacheKey is the concert_cache key for one genre in one cell. It lives
// in the tm: namespace so the janitor's prune expires it like any other
// listing and the discover view can read it -- the payload is the same
// []ticketmaster.Event shape -- and the "genre-" slot keeps it apart from
// every per-artist key, whose slot is a Spotify ID or a "tm:" pick ID.
func GenreCacheKey(genreID string, cell Location) string {
	return cacheKey(CachePrefixTicketmaster, "genre-"+genreID, cell)
}

// SearchGenres builds the genre part of a user's feed: one listing per genre
// for the user's grid cell, filtered to their own circle, merged, and capped.
//
// horizon is the venue-local day before which the result is complete, "" when
// nothing was cut. Every listing that hit its page cap contributes its own
// horizon and the earliest wins, then MaxGenreFeedConcerts may pull it
// earlier still; rows on or after it are dropped, so the feed never holds a
// partial day it then presents as whole.
//
// Failures follow Search's rules. A Ticketmaster error for one genre is
// logged and that genre contributes nothing, as an artist's would. Quota
// exhaustion and an expired budget come back as *IncompleteError, with the
// results gathered so far, so the scan marks its snapshot incomplete exactly
// as it does for artists.
func SearchGenres(ctx context.Context, d SearchDeps, genres []GenreTarget, loc Location) ([]Concert, string, error) {
	if len(genres) == 0 {
		return nil, "", nil
	}
	if d.CacheTTL == 0 {
		d.CacheTTL = DefaultCacheTTL
	}
	cell := GenreCell(loc)
	m := NewMerger()
	horizon := ""
	skipped := 0
	for _, g := range genres {
		if g.ID == "" {
			continue
		}
		evs, h, err := loadOrFetchGenre(ctx, d, g.ID, cell)
		if err != nil {
			switch {
			case errors.Is(err, errRateCapped):
				// Recorded as a denial on the reservation; reported below.
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				skipped++
			default:
				slog.Warn("genre listing failed", "genre", g.Name, "err", err)
			}
			continue
		}
		if h != "" && (horizon == "" || h < horizon) {
			horizon = h
		}
		for _, e := range evs {
			for _, c := range genreConcerts(e, g) {
				m.Add(c)
			}
		}
	}

	out := Near(m.All(), loc)
	out, horizon = capGenreFeed(out, horizon, MaxGenreFeedConcerts)

	capped := rate.FromContext(ctx).AnyExhausted()
	if skipped > 0 || capped {
		return out, horizon, &IncompleteError{
			SkippedArtists: skipped,
			TotalArtists:   len(genres),
			RateCapped:     capped,
			Cause:          ctx.Err(),
		}
	}
	return out, horizon, nil
}

// capGenreFeed drops rows on or after horizon, then cuts to max rows on a day
// boundary, returning the rows kept and the horizon that now describes them.
// cs must be date-sorted (Merger.All is).
//
// The cut is at a day boundary for the same reason the horizon is exclusive:
// a feed that keeps three of a night's eight shows says nothing true about
// that night. If a single day alone exceeds max, there is no boundary to cut
// at, and the first max rows are kept with the horizon on that day; the claim
// "complete before the horizon" still holds.
func capGenreFeed(cs []Concert, horizon string, limit int) ([]Concert, string) {
	if horizon != "" {
		kept := cs[:0:0]
		for _, c := range cs {
			if c.LocalDay() < horizon {
				kept = append(kept, c)
			}
		}
		cs = kept
	}
	if len(cs) <= limit {
		return cs, horizon
	}
	// LocalDay order can differ from instant order by a day across
	// timezones; re-sort on the day so the boundary is a real one.
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].LocalDay() < cs[j].LocalDay() })
	cut := cs[limit].LocalDay()
	n := sort.Search(len(cs), func(i int) bool { return cs[i].LocalDay() >= cut })
	if n == 0 {
		n = limit
	}
	return cs[:n], cut
}

// loadOrFetchGenre returns one genre's listing for one cell, from the cache or
// from Ticketmaster at up to ticketmaster.MaxGenrePages permits.
//
// This is the written exception to loadOrFetchTM's "only a complete result may
// be cached": a listing cut at its page cap is cached, with its horizon,
// because a date-sorted prefix is correct up to that day. A listing cut by a
// refused permit or a failed page is not -- that stop is not the one asked
// for, and caching it would serve a shorter listing for the whole TTL.
func loadOrFetchGenre(ctx context.Context, d SearchDeps, genreID string, cell Location) ([]ticketmaster.Event, string, error) {
	key := GenreCacheKey(genreID, cell)
	if blob, horizon, ok, err := db.GetCachedListing(ctx, d.Pool, key, d.CacheTTL); err != nil {
		slog.Warn("cache read failed", "key", key, "err", err)
	} else if ok {
		var out []ticketmaster.Event
		if err := json.Unmarshal(blob, &out); err == nil {
			return out, horizon, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !rate.Allow(ctx, rate.SourceTicketmaster) {
		return nil, "", errRateCapped
	}
	morePages := func() bool { return rate.Allow(ctx, rate.SourceTicketmaster) }
	evs, horizon, cacheable, err := d.TM.SearchGenreEvents(ctx, genreID, cell.Latitude, cell.Longitude, cell.RadiusMiles, morePages)
	if err != nil {
		if len(evs) == 0 {
			return nil, "", fmt.Errorf("tm genre: %w", err)
		}
		slog.Warn("tm genre listing partially fetched", "genre", genreID, "events", len(evs), "err", err)
	}
	if cacheable && err == nil {
		if blob, err := json.Marshal(evs); err == nil {
			_ = db.SaveCachedListing(ctx, d.Pool, key, blob, horizon)
		}
	}
	return evs, horizon, nil
}

// genreConcerts expands one listed event into a Concert per act. Unlike a
// pick, the acts come from Ticketmaster's lineup, not from anything the user
// chose: an act with an attraction carries its tm: ID so the bell works on
// it, and an event with no attraction at all -- 30 of 74 New York country
// events measured that way -- is one Concert named after the event.
//
// The genre is the event's own, falling back to the genre that was picked,
// so the facet the card lands under is Ticketmaster's word for it.
func genreConcerts(e ticketmaster.Event, g GenreTarget) []Concert {
	type act struct{ id, name string }
	acts := make([]act, 0, len(e.Lineup))
	names := make([]string, 0, len(e.Lineup))
	for _, at := range e.Lineup {
		if at.Name == "" {
			continue
		}
		acts = append(acts, act{id: at.ID, name: at.Name})
		names = append(names, at.Name)
	}
	if len(acts) == 0 {
		if e.Name == "" {
			return nil
		}
		acts = []act{{name: e.Name}}
	}
	if len(acts) > DiscoverMaxActs {
		acts = acts[:DiscoverMaxActs]
	}
	genre := e.Genre
	if genre == "" {
		genre = g.Name
	}
	out := make([]Concert, 0, len(acts))
	for _, a := range acts {
		ref := ArtistRef{Name: a.name}
		if a.id != "" {
			ref.ID = TicketmasterArtistID(a.id)
		}
		if genre != "" {
			ref.Genres = []string{genre}
		}
		c := Concert{
			Artist:    ref,
			Date:      e.Start,
			LocalDate: e.LocalDate,
			Venue:     e.Venue.Name,
			City:      e.Venue.City,
			State:     e.Venue.State,
			Country:   e.Venue.Country,
			Latitude:  e.Venue.Latitude,
			Longitude: e.Venue.Longitude,
			Links:     []TicketLink{{Source: SourceTicketmaster, URL: e.URL}},

			EventName:  e.Name,
			IsFestival: e.IsFestival,
		}
		c.Billing = billingOf(names, a.name)
		c.DedupKey = DedupKey(c.Artist.Name, c.LocalDay(), c.Venue, c.City)
		out = append(out, c)
	}
	return out
}
