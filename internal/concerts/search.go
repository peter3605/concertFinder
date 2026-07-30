package concerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/bandsintown"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/spotify"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// bitLockedOutMarker identifies the AWS-flavored 403 that Bandsintown's public
// API is currently returning for every request. Logging each occurrence
// generates hundreds of identical WARN lines per search with no signal — a
// single startup notice would be nicer, but this is the least-invasive fix.
const bitLockedOutMarker = "explicit deny in an identity-based policy"

// Location is the search origin (design §10.1: hardcoded in Phase 1).
type Location struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	RadiusMiles int     `json:"radius_miles"`
}

// Fallbacker is the small-artist coverage escalation from design §5.4.
// Implemented by internal/fallback.Chain. Nil-safe: if unset, the fallback
// stage is skipped entirely.
type Fallbacker interface {
	FindEvents(ctx context.Context, artist spotify.ScoredArtist, loc Location) []Concert
}

// SearchDeps holds the wired-up clients + DB pool for the fan-out.
type SearchDeps struct {
	Pool             *pgxpool.Pool
	TM               *ticketmaster.Client
	BIT              *bandsintown.Client
	CacheTTL         time.Duration // §7.3 = 4 hours
	Parallelism      int           // §8.1 = 10
	Fallback         Fallbacker    // nil = Phase 1 behavior (no fallback)
	MinFallbackScore float64       // artists with Score < this bypass the fallback
	// RateLedger enforces per-user daily caps on TM/BIT/Songkick (design §8.3).
	// Nil = no enforcement. User is read from ctx via rate.UserFromContext,
	// which the scan worker sets before calling into Search.
	RateLedger *rate.Ledger
}

// Search fans out to TM + BIT for each artist, respects ctx, and returns
// deduped concerts sorted by date. Backwards-compatible wrapper over
// StreamSearch for callers that want blocking-full behavior.
func Search(ctx context.Context, d SearchDeps, artists []spotify.ScoredArtist, loc Location) ([]Concert, error) {
	m := NewMerger()
	if err := StreamSearch(ctx, d, artists, loc, m); err != nil {
		return nil, err
	}
	return m.All(), nil
}

// StreamSearch drives the same §8.1 fan-out but writes results into a
// caller-supplied Merger as each artist completes. Returns when every
// artist goroutine has finished (or ctx expires). Callers that want to
// expose intermediate state to a client (streaming poll endpoint) share the
// merger with a reader.
func StreamSearch(ctx context.Context, d SearchDeps, artists []spotify.ScoredArtist, loc Location, m *Merger) error {
	if d.Parallelism <= 0 {
		d.Parallelism = 10
	}
	if d.CacheTTL == 0 {
		d.CacheTTL = 4 * time.Hour
	}
	sem := make(chan struct{}, d.Parallelism)

	var wg sync.WaitGroup
	for _, a := range artists {
		if a.ID == "" || a.Name == "" {
			continue
		}
		a := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			batch, err := searchOne(ctx, d, a, loc)
			if err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					slog.Warn("artist search failed", "artist", a.Name, "err", err)
				}
				return
			}
			for _, c := range batch {
				m.Add(c)
			}
		}()
	}
	wg.Wait()
	return nil
}

// searchOne resolves + queries TM and BIT for one artist in parallel.
func searchOne(ctx context.Context, d SearchDeps, a spotify.ScoredArtist, loc Location) ([]Concert, error) {
	resolution, hit, err := db.GetArtistResolution(ctx, d.Pool, a.ID)
	if err != nil {
		slog.Warn("resolution lookup failed", "artist", a.Name, "err", err)
	}

	// If we've never resolved this artist, do it once — TM only.
	// BIT accepts free-form names, no lookup step required.
	if !hit {
		attractionID, err := d.TM.ResolveAttraction(ctx, a.Name)
		if err != nil {
			slog.Warn("TM resolve failed", "artist", a.Name, "err", err)
			// Continue without TM this round; skip persisting so we retry later.
		} else {
			resolution = db.ArtistResolution{
				SpotifyArtistID:          a.ID,
				TicketmasterAttractionID: attractionID,
				BandsintownName:          a.Name,
			}
			if err := db.UpsertArtistResolution(ctx, d.Pool, resolution); err != nil {
				slog.Warn("resolution save failed", "artist", a.Name, "err", err)
			}
		}
	} else if resolution.BandsintownName == "" {
		resolution.BandsintownName = a.Name
	}

	// TM + BIT run independently. A failure in one source must not cancel or
	// discard the other: BIT has been intermittently returning 403 for the
	// entire public API surface, and losing all TM results because BIT is
	// down defeats the point of having two sources.
	var (
		tmEvents  []ticketmaster.Event
		bitEvents []bandsintown.Event
	)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if resolution.TicketmasterAttractionID == "" {
			return
		}
		evs, err := loadOrFetchTM(ctx, d, a.ID, resolution.TicketmasterAttractionID, loc)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Warn("tm fetch failed", "artist", a.Name, "err", err)
			}
			return
		}
		tmEvents = evs
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		name := resolution.BandsintownName
		if name == "" {
			name = a.Name
		}
		evs, err := loadOrFetchBIT(ctx, d, a.ID, name, loc)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				// ctx cleanup — not actionable
			case strings.Contains(err.Error(), bitLockedOutMarker):
				// BIT's public API is currently 403'ing every request. Don't
				// spam the log with one line per artist for a known outage.
			default:
				slog.Warn("bit fetch failed", "artist", a.Name, "err", err)
			}
			return
		}
		bitEvents = evs
	}()

	wg.Wait()

	out := make([]Concert, 0, len(tmEvents)+len(bitEvents))
	for _, e := range tmEvents {
		out = append(out, tmEventToConcert(e, a))
	}
	for _, e := range bitEvents {
		out = append(out, bitEventToConcert(e, a))
	}
	if len(out) == 0 && d.Fallback != nil && a.Score >= d.MinFallbackScore {
		fb := d.Fallback.FindEvents(ctx, a, loc)
		// Fallback emits concerts with only artist name/ID; attach genres from
		// the ScoredArtist so downstream filters still work.
		for i := range fb {
			if len(fb[i].Artist.Genres) == 0 && len(a.Genres) > 0 {
				fb[i].Artist.Genres = a.Genres
			}
		}
		out = append(out, fb...)
	}
	return out, nil
}

func cacheKey(source, artistID string, loc Location) string {
	return source + ":" + artistID + ":" +
		strconv.FormatFloat(loc.Latitude, 'f', 4, 64) + "," +
		strconv.FormatFloat(loc.Longitude, 'f', 4, 64) + "," +
		strconv.Itoa(loc.RadiusMiles)
}

func loadOrFetchTM(ctx context.Context, d SearchDeps, artistID, attractionID string, loc Location) ([]ticketmaster.Event, error) {
	key := cacheKey("tm", artistID, loc)
	if blob, ok, err := db.GetCachedConcerts(ctx, d.Pool, key, d.CacheTTL); err != nil {
		slog.Warn("cache read failed", "key", key, "err", err)
	} else if ok {
		var out []ticketmaster.Event
		if err := json.Unmarshal(blob, &out); err == nil {
			return out, nil
		}
	}
	// Cache miss — this becomes a real upstream call. Charge the user's
	// TM ledger; if they're over the daily cap treat as empty for the
	// remainder of the day.
	if d.RateLedger != nil && !d.RateLedger.AllowFromContext(ctx, rate.SourceTicketmaster) {
		return nil, nil
	}
	evs, err := d.TM.SearchEvents(ctx, attractionID, loc.Latitude, loc.Longitude, loc.RadiusMiles)
	if err != nil {
		return nil, fmt.Errorf("tm: %w", err)
	}
	if blob, err := json.Marshal(evs); err == nil {
		_ = db.SaveCachedConcerts(ctx, d.Pool, key, blob)
	}
	return evs, nil
}

func loadOrFetchBIT(ctx context.Context, d SearchDeps, artistID, name string, loc Location) ([]bandsintown.Event, error) {
	key := cacheKey("bit", artistID, loc)
	if blob, ok, err := db.GetCachedConcerts(ctx, d.Pool, key, d.CacheTTL); err != nil {
		slog.Warn("cache read failed", "key", key, "err", err)
	} else if ok {
		var out []bandsintown.Event
		if err := json.Unmarshal(blob, &out); err == nil {
			return out, nil
		}
	}
	if d.RateLedger != nil && !d.RateLedger.AllowFromContext(ctx, rate.SourceBandsintown) {
		return nil, nil
	}
	evs, err := d.BIT.ArtistEvents(ctx, name, loc.Latitude, loc.Longitude, loc.RadiusMiles)
	if err != nil {
		return nil, fmt.Errorf("bit: %w", err)
	}
	if blob, err := json.Marshal(evs); err == nil {
		_ = db.SaveCachedConcerts(ctx, d.Pool, key, blob)
	}
	return evs, nil
}

func artistRefFromScored(a spotify.ScoredArtist) ArtistRef {
	return ArtistRef{ID: a.ID, Name: a.Name, Genres: a.Genres}
}

func tmEventToConcert(e ticketmaster.Event, a spotify.ScoredArtist) Concert {
	c := Concert{
		Artist:    artistRefFromScored(a),
		Date:      e.Start,
		Venue:     e.Venue.Name,
		City:      e.Venue.City,
		State:     e.Venue.State,
		Country:   e.Venue.Country,
		Latitude:  e.Venue.Latitude,
		Longitude: e.Venue.Longitude,
		Links:     []TicketLink{{Source: SourceTicketmaster, URL: e.URL}},
	}
	c.DedupKey = DedupKey(c.Artist.Name, c.Date, c.Venue, c.City)
	return c
}

func bitEventToConcert(e bandsintown.Event, a spotify.ScoredArtist) Concert {
	c := Concert{
		Artist:    artistRefFromScored(a),
		Date:      e.Datetime,
		Venue:     e.Venue.Name,
		City:      e.Venue.City,
		State:     e.Venue.Region,
		Country:   e.Venue.Country,
		Latitude:  e.Venue.Latitude,
		Longitude: e.Venue.Longitude,
		Links:     []TicketLink{{Source: SourceBandsintown, URL: e.URL}},
	}
	c.DedupKey = DedupKey(c.Artist.Name, c.Date, c.Venue, c.City)
	return c
}
