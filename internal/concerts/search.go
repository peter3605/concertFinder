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
	"sync/atomic"
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

// errRateCapped marks a source that was skipped because the user's daily
// quota for it is spent. Distinct from "returned no events" on purpose: an
// empty result from a capped source must not be read as evidence that the
// artist has no shows, because that would escalate the artist into the far
// more expensive Phase 2 fallback chain (MusicBrainz at 1 req/sec, robots
// fetches, page scraping, Nominatim) for no reason.
var errRateCapped = errors.New("concerts: per-user rate cap reached")

// NegativeResolutionTTL is how long a "Ticketmaster has no attraction for
// this artist" result stays trusted. Resolution requires an exact
// case-insensitive name match, so artists whose TM spelling differs from
// Spotify's ("Tyler, The Creator") resolve to nothing — and artists simply
// sign to TM later. Without a TTL either case is a permanent exclusion.
const NegativeResolutionTTL = 30 * 24 * time.Hour

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
//
// Per-user daily quota (design §8.3) is not a field here: it rides on the
// context as a set of pre-charged reservations that the scan worker takes
// out before calling in. See internal/rate.
type SearchDeps struct {
	Pool             *pgxpool.Pool
	TM               *ticketmaster.Client
	BIT              *bandsintown.Client
	// CacheTTL is how long a cached per-artist upstream response is trusted.
	// Zero means DefaultCacheTTL.
	CacheTTL time.Duration
	Parallelism      int           // §8.1 = 10
	Fallback         Fallbacker    // nil = Phase 1 behavior (no fallback)
	MinFallbackScore float64       // artists with Score < this bypass the fallback
	// FallbackBudget caps the wall-clock time *all* fallback work in one
	// scan may consume, across every artist. It exists because the chain's
	// external lookups are globally serialized at 1 req/sec (MusicBrainz,
	// Nominatim), so their cost scales with the number of escalating
	// artists and not with parallelism: measured at ~250s of MusicBrainz
	// plus ~86s of Nominatim on a 200-artist profile, against a 300s
	// ScanBudget. Unbounded, the fallback alone can consume the entire
	// scan and starve TM/BIT.
	//
	// Zero means DefaultFallbackBudget. Negative disables the fallback.
	FallbackBudget time.Duration
	// FallbackGate rations the process-wide fallback slot across concurrent
	// scans. Must be the *same* gate for every scan in the process to mean
	// anything. Nil = no gating (every scan may use the fallback).
	FallbackGate *FallbackGate
}

// DefaultCacheTTL is how long a cached per-artist upstream response stays
// trusted. Design §7.3 originally said 4 hours, which turned out to be the
// binding constraint on coverage rather than a freshness choice: a scan
// covers MaxScoredArtists (200) artists, so once the cache lapses a full
// scan needs ~200 fresh Ticketmaster calls against a 250/day per-user cap.
// At 4h the cache expired several times a day and every expiry cost a fresh
// call, so scans routinely ran out of quota partway and returned a fraction
// of the user's shows.
//
// 12h means at most one quota-spending scan per half-day, with every SWR
// refresh in between served from cache. Event listings do not churn on an
// hourly basis, so this trades no freshness anyone can perceive for
// coverage everyone can.
const DefaultCacheTTL = 12 * time.Hour

// DefaultFallbackBudget is the fallback's share of a scan when unset. Sized
// well under ScanBudget so TM/BIT — the sources that actually produce most
// results — always finish.
const DefaultFallbackBudget = 60 * time.Second

// FallbackAdmissionWait is how long a scan will wait for the process-wide
// fallback slot before giving up on the fallback for this run. Short on
// purpose: the holder typically keeps the slot for a full budget, so a long
// wait would just burn this scan's own ScanBudget doing nothing. A few
// seconds is enough to absorb the case where the holder is about to finish.
const FallbackAdmissionWait = 5 * time.Second

// FallbackGate admits a bounded number of scans to the fallback chain at
// once, process-wide.
//
// It exists because the fallback's cost is not per-scan: MusicBrainz and
// Nominatim are each rationed by a single process-global 1 req/sec
// turnstile shared by every scan, while the fallback *budget* is per-scan
// wall-clock. Without a gate, N concurrent scans each believe they have a
// full budget but split one turnstile N ways — a scan that resolves ~55
// artists alone resolves ~11 when five run together. Nothing errors; the
// small-artist coverage just quietly thins out as users are added.
//
// Rationing admission instead of letting everyone in concentrates the same
// total throughput into scans that can actually use it. A scan that isn't
// admitted skips the fallback outright rather than crawling through it, and
// says so in the logs. Because resolver results are cached in Postgres
// (mb_url_cache, venue_geo_cache) and scans repeat nightly, coverage still
// accrues across runs.
//
// A nil *FallbackGate admits everyone, which is what tests and
// single-scan deployments want.
type FallbackGate struct {
	slots chan struct{}
}

// NewFallbackGate builds a gate admitting n concurrent scans. n <= 0 means
// unlimited (no gating). One is the meaningful default: the turnstiles
// serialize anyway, so a second admitted scan buys throughput for neither.
func NewFallbackGate(n int) *FallbackGate {
	if n <= 0 {
		return nil
	}
	return &FallbackGate{slots: make(chan struct{}, n)}
}

// Acquire tries to claim a slot, waiting at most `wait`. A wait of zero or
// less means "take it only if it's free right now". Returns whether
// admission was granted and a release function that is always safe to call
// — including when admission was refused, and more than once.
func (g *FallbackGate) Acquire(ctx context.Context, wait time.Duration) (bool, func()) {
	if g == nil {
		return true, func() {}
	}
	refused := func() (bool, func()) { return false, func() {} }
	admitted := func() (bool, func()) {
		var once sync.Once
		return true, func() { once.Do(func() { <-g.slots }) }
	}
	if err := ctx.Err(); err != nil {
		return refused()
	}
	if wait <= 0 {
		// Non-blocking. Note this cannot be folded into the timed path with
		// a nil timer channel: a nil channel blocks forever in select, which
		// turns "don't wait" into "wait indefinitely".
		select {
		case g.slots <- struct{}{}:
			return admitted()
		default:
			return refused()
		}
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case g.slots <- struct{}{}:
		return admitted()
	case <-ctx.Done():
		return refused()
	case <-t.C:
		return refused()
	}
}

// resolveFallbackBudget maps a configured budget onto an effective one:
// zero means "unset, use the default", negative means "disabled".
func resolveFallbackBudget(configured time.Duration) time.Duration {
	if configured == 0 {
		return DefaultFallbackBudget
	}
	return configured
}

// fallbackContext derives the scan-wide fallback deadline. A non-positive
// budget yields an already-cancelled context, which every escalation site
// treats as "no budget left" — so disabling the fallback needs no separate
// branch at the call sites.
func fallbackContext(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		fbCtx, cancel := context.WithCancel(ctx)
		cancel()
		return fbCtx, cancel
	}
	return context.WithTimeout(ctx, budget)
}

// IncompleteError reports that a search finished without covering every
// artist. Callers get the partial results alongside it and decide what to
// do — the scan worker persists them but marks the snapshot incomplete so
// the SWR handler keeps asking for a refresh instead of trusting a
// half-empty result for the full staleness window.
type IncompleteError struct {
	// SkippedArtists never ran, or bailed out when the budget expired.
	SkippedArtists int
	// TotalArtists is the size of the input set, for context in logs.
	TotalArtists int
	// RateCapped is set when at least one upstream source ran out of the
	// user's daily quota partway through.
	RateCapped bool
	// Cause is the context error when the budget expired, else nil.
	Cause error
}

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("concerts: incomplete scan (%d/%d artists skipped, rate_capped=%v)",
		e.SkippedArtists, e.TotalArtists, e.RateCapped)
}

func (e *IncompleteError) Unwrap() error { return e.Cause }

// Search fans out to TM + BIT for each artist, respects ctx, and returns
// deduped concerts sorted by date.
//
// A non-nil error does NOT mean the returned slice is unusable: on an
// *IncompleteError the results gathered so far come back too. Only a caller
// that needs completeness (the snapshot writer) should act on the error.
func Search(ctx context.Context, d SearchDeps, artists []spotify.ScoredArtist, loc Location) ([]Concert, error) {
	if d.Parallelism <= 0 {
		d.Parallelism = 10
	}
	if d.CacheTTL == 0 {
		d.CacheTTL = DefaultCacheTTL
	}

	// Drop unusable entries up front so the skipped/total accounting below
	// describes real work rather than input noise.
	usable := make([]spotify.ScoredArtist, 0, len(artists))
	for _, a := range artists {
		if a.ID == "" || a.Name == "" {
			continue
		}
		usable = append(usable, a)
	}

	// One deadline shared by every artist's fallback work, so the cap is on
	// the scan's total fallback spend rather than per-artist (200 artists ×
	// a per-artist limit would be no cap at all). TM/BIT keep the full ctx.
	budget := resolveFallbackBudget(d.FallbackBudget)

	// Claim the process-wide fallback slot. Unadmitted scans skip the
	// fallback rather than share a turnstile that gives them a fraction of
	// the throughput their budget implies.
	admitted := true
	if budget > 0 && d.Fallback != nil {
		var release func()
		admitted, release = d.FallbackGate.Acquire(ctx, FallbackAdmissionWait)
		defer release()
		if !admitted {
			budget = -1 // disables the fallback for this scan
		}
	}

	fallbackCtx, cancelFallback := fallbackContext(ctx, budget)
	defer cancelFallback()

	m := NewMerger()
	sem := make(chan struct{}, d.Parallelism)
	var skipped atomic.Int64
	var fallbackSkipped atomic.Int64
	var wg sync.WaitGroup

	for _, a := range usable {
		a := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				// Budget expired before this artist got a slot. This is the
				// case that used to vanish silently and leave a partial
				// snapshot looking complete.
				skipped.Add(1)
				return
			}
			defer func() { <-sem }()

			batch, err := searchOne(ctx, fallbackCtx, d, a, loc, &fallbackSkipped)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					skipped.Add(1)
				} else {
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

	out := m.All()
	nSkipped := int(skipped.Load())
	capped := rate.FromContext(ctx).AnyExhausted()

	// Falling short on fallback coverage is expected on a cold profile and is
	// deliberately NOT an incompleteness signal. The fallback is best-effort
	// enrichment for artists the primary sources missed; marking the scan
	// incomplete would set complete=false on essentially every cold scan,
	// which the SWR handler reads as "always stale" — an endless rescan loop
	// that spends more budget to again run out of budget.
	//
	// It does get logged, and the two causes are named separately: "the
	// budget ran out" is a tuning signal, while "another scan held the slot"
	// is a concurrency signal that means this deployment is running more
	// simultaneous scans than the shared 1 req/sec turnstiles can feed.
	if n := int(fallbackSkipped.Load()); n > 0 {
		if !admitted {
			slog.Info("fallback skipped: another scan holds the process-wide slot",
				"artists_not_escalated", n,
				"total_artists", len(usable),
			)
		} else {
			slog.Info("fallback budget exhausted",
				"artists_not_escalated", n,
				"total_artists", len(usable),
				"budget", budget,
			)
		}
	}

	if nSkipped > 0 || capped {
		return out, &IncompleteError{
			SkippedArtists: nSkipped,
			TotalArtists:   len(usable),
			RateCapped:     capped,
			Cause:          ctx.Err(),
		}
	}
	return out, nil
}

// searchOne resolves + queries TM and BIT for one artist in parallel.
// Returns a context error when the artist could not be covered because the
// scan budget expired; individual source failures (a BIT 403, a TM 5xx) are
// logged and folded into a partial result rather than failing the artist.
//
// fallbackCtx is the scan-wide fallback deadline (see SearchDeps.FallbackBudget);
// it is a child of ctx, so it is never the longer-lived of the two.
// fallbackSkipped counts artists that would have escalated but couldn't
// because that budget was gone.
func searchOne(ctx, fallbackCtx context.Context, d SearchDeps, a spotify.ScoredArtist, loc Location, fallbackSkipped *atomic.Int64) ([]Concert, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resolution, hit, err := db.GetArtistResolution(ctx, d.Pool, a.ID)
	if err != nil {
		slog.Warn("resolution lookup failed", "artist", a.Name, "err", err)
	}

	// Resolve against TM when we've never looked this artist up, or when a
	// previous negative result has aged past NegativeResolutionTTL.
	if needsTMResolution(resolution, hit) {
		// Resolution is the loudest part of a scan for a new user — up to
		// one call per artist — so it spends quota like any other TM call.
		// Leaving it uncounted made RATE_CAP_TM_PER_USER_DAILY unenforceable
		// against exactly the traffic it exists to bound.
		if !rate.Allow(ctx, rate.SourceTicketmaster) {
			return nil, nil
		}
		attractionID, err := d.TM.ResolveAttraction(ctx, a.Name)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
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
		// Set when a source was skipped for quota rather than returning
		// nothing. Gates the fallback escalation below.
		capped atomic.Bool
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
			switch {
			case errors.Is(err, errRateCapped):
				capped.Store(true)
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				// ctx cleanup — not actionable
			default:
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
			case errors.Is(err, errRateCapped):
				capped.Store(true)
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

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out := make([]Concert, 0, len(tmEvents)+len(bitEvents))
	for _, e := range tmEvents {
		out = append(out, tmEventToConcert(e, a))
	}
	for _, e := range bitEvents {
		out = append(out, bitEventToConcert(e, a))
	}
	// Escalate to the fallback chain only when the primary sources actually
	// reported nothing. If either was skipped for quota we don't know that
	// yet, and the fallback is far more expensive than what we just declined
	// to spend.
	if len(out) == 0 && !capped.Load() && d.Fallback != nil && a.Score >= d.MinFallbackScore {
		// Don't even enter the chain once the scan-wide fallback budget is
		// spent — every lookup inside would queue on a 1 req/sec turnstile
		// only to be cancelled.
		if fallbackCtx.Err() != nil {
			fallbackSkipped.Add(1)
			return out, nil
		}
		fb := d.Fallback.FindEvents(fallbackCtx, a, loc)
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

// needsTMResolution decides whether to (re-)ask Ticketmaster for an
// attraction ID. A row with an ID is permanent; a row without one is a
// negative cache entry that expires, so an artist who joins TM later — or
// whose name we simply failed to match — gets another chance.
func needsTMResolution(r db.ArtistResolution, hit bool) bool {
	if !hit {
		return true
	}
	if r.TicketmasterAttractionID != "" {
		return false
	}
	return time.Since(r.ResolvedAt) > NegativeResolutionTTL
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
	// Cache miss — this becomes a real upstream call, so it spends quota.
	if !rate.Allow(ctx, rate.SourceTicketmaster) {
		return nil, errRateCapped
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
	if !rate.Allow(ctx, rate.SourceBandsintown) {
		return nil, errRateCapped
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
