package http

import (
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/jobs"
)

// ConcertsHandler serves /me/concerts with stale-while-revalidate semantics.
// Requests never trigger a synchronous fan-out; they return the last
// completed snapshot immediately and enqueue a background refresh when the
// snapshot is stale or missing.
//
// Reads pass through an in-process SnapshotCache to skip the DB fetch +
// JSON assembly on repeat requests. The cache is keyed on
// (user, location, computed_at), so a new scan naturally invalidates the
// stale entry the next time it's looked up.
type ConcertsHandler struct {
	Pool               *pgxpool.Pool
	River              *river.Client[pgx.Tx]
	FallbackLocation   concerts.Location
	SnapshotStaleAfter time.Duration
	SnapshotCache      *SnapshotCache // nil = cache disabled
}

type concertsResponse struct {
	Location concerts.Location `json:"location"`
	// Count is a number of events, matching one card each — not a number of
	// artist matches, which would overcount every festival.
	Count      int              `json:"count"`
	Events     []concerts.Event `json:"events"`
	Facets     facetSet         `json:"facets"`
	ComputedAt *time.Time       `json:"computed_at,omitempty"`
	Refreshing bool             `json:"refreshing"`
	// Complete is false when the scan behind these results didn't cover
	// every artist. Surfaced so the UI can distinguish "your area is quiet"
	// from "we only got partway through" — otherwise a truncated feed is
	// indistinguishable from a genuinely empty one.
	Complete bool `json:"complete"`
	// RetryAfter is set when the shortfall was the user's daily upstream
	// quota, which only resets on the hour named here. Lets the UI say when
	// more results are expected instead of implying an imminent refresh.
	RetryAfter *time.Time `json:"retry_after,omitempty"`
}

type facetSet struct {
	Genres []facet `json:"genres"`
	Venues []facet `json:"venues"`
}

type facet struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

func (h *ConcertsHandler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// The user's location, saved set, and subscribed set are three
	// independent reads that the response needs before it can be built.
	// Issuing them concurrently keeps this endpoint — which the frontend
	// polls every 10s during a refresh — from paying for them serially.
	var (
		loc        = h.FallbackLocation
		saved      map[string]struct{}
		subscribed map[string]struct{}
	)
	var pre sync.WaitGroup
	pre.Add(3)
	go func() {
		defer pre.Done()
		userLoc, hit, err := db.GetUserLocation(r.Context(), h.Pool, u.ID)
		if err != nil {
			slog.Warn("concerts: user location lookup failed", "err", err, "user", u.ID)
			return
		}
		if hit {
			loc = concerts.Location{
				Latitude:    userLoc.Latitude,
				Longitude:   userLoc.Longitude,
				RadiusMiles: userLoc.RadiusMiles,
			}
		}
	}()
	go func() {
		defer pre.Done()
		s, err := db.GetSavedDedupKeys(r.Context(), h.Pool, u.ID)
		if err != nil {
			slog.Warn("concerts: saved lookup failed", "err", err, "user", u.ID)
			return
		}
		saved = s
	}()
	go func() {
		defer pre.Done()
		s, err := db.GetSubscribedArtistIDs(r.Context(), h.Pool, u.ID)
		if err != nil {
			slog.Warn("concerts: subscribed lookup failed", "err", err, "user", u.ID)
			return
		}
		subscribed = s
	}()
	pre.Wait()

	locKey := jobs.LocationKey(loc)

	var (
		found      []concerts.Concert
		computedAt *time.Time
		hasSnap    bool
		// complete is false when the scan behind this snapshot didn't
		// cover every artist; such a snapshot is always treated as stale.
		complete bool
		// retryAfter, when set, is the earliest time a re-scan could do
		// any better (the user's daily upstream quota resets then).
		retryAfter *time.Time
	)
	if snap, hit, err := db.GetConcertSnapshot(r.Context(), h.Pool, u.ID, locKey); err != nil {
		slog.Warn("concerts: snapshot read failed", "err", err, "user", u.ID)
	} else if hit {
		hasSnap = true
		complete = snap.Complete
		retryAfter = snap.RetryAfter
		t := snap.ComputedAt
		computedAt = &t
		// Cache lookup keyed on (user, location, computed_at). A new scan
		// bumps computed_at and misses the cache; the new payload is
		// installed on next request.
		if h.SnapshotCache != nil {
			if cs, ok := h.SnapshotCache.Get(u.ID, locKey, snap.ComputedAt); ok {
				found = cs
			}
		}
		if found == nil {
			blobs, err := db.GetConcertsByDedupKeys(r.Context(), h.Pool, snap.DedupKeys)
			if err != nil {
				slog.Warn("concerts: load concerts failed", "err", err, "user", u.ID)
				hasSnap = false
			} else if assembled, err := concerts.AssembleByKey(snap.DedupKeys, blobs); err != nil {
				slog.Warn("concerts: assemble failed", "err", err, "user", u.ID)
				hasSnap = false
			} else {
				found = assembled
				if h.SnapshotCache != nil {
					h.SnapshotCache.Put(u.ID, locKey, snap.ComputedAt, assembled)
				}
			}
		}
	}

	// SWR: enqueue a background scan when the snapshot is missing, older
	// than the staleness window, or known to be partial. River's uniqueness
	// (declared on ScanConcertsArgs) guarantees we don't pile up duplicate
	// jobs if the user refreshes rapidly.
	stale := !hasSnap ||
		!complete ||
		(computedAt != nil && time.Since(*computedAt) > h.SnapshotStaleAfter)

	// ...unless the last scan told us to wait. A quota-capped scan stays
	// incomplete — and therefore permanently "stale" — until the daily
	// ledger resets, so without this the 10s poll loop re-enqueues a scan
	// that cannot succeed, every 10s, for the rest of the day.
	if retryAfter != nil && time.Now().Before(*retryAfter) {
		stale = false
	}

	refreshing := false
	if stale && h.River != nil {
		args := jobs.ScanConcertsArgs{
			UserID:      u.ID,
			Latitude:    loc.Latitude,
			Longitude:   loc.Longitude,
			RadiusMiles: loc.RadiusMiles,
		}
		// Uniqueness is declared on ScanConcertsArgs.InsertOpts: one scan in
		// flight per (user, location). Supplying opts here would override
		// that and reopen the concurrent-scan starvation this fixed.
		if _, err := h.River.Insert(r.Context(), args, nil); err != nil {
			slog.Warn("concerts: scan enqueue failed", "err", err, "user", u.ID)
		} else {
			refreshing = true
		}
	}

	facets := computeFacets(found)
	filters := parseFilters(r, loc)
	filtered := concerts.Apply(found, filters)

	// Overlay per-user saved + subscribed status. Both sets are small
	// (bounded by the user's own picks) so an O(n) tag is fine; they were
	// read concurrently above.
	for i := range filtered {
		if _, ok := saved[filtered[i].DedupKey]; ok {
			filtered[i].Saved = true
		}
		if _, ok := subscribed[filtered[i].Artist.ID]; ok {
			filtered[i].Subscribed = true
		}
	}
	if r.URL.Query().Get("saved_only") == "true" {
		kept := filtered[:0]
		for _, c := range filtered {
			if c.Saved {
				kept = append(kept, c)
			}
		}
		filtered = kept
	}

	// Group last, after filtering and tagging: an act carries its own saved
	// and subscribed flags onto the card, and an event survives a filter if
	// any of its acts does.
	events := concerts.GroupEvents(filtered)

	writeJSON(w, concertsResponse{
		Location:   loc,
		Count:      len(events),
		Events:     events,
		Facets:     facets,
		ComputedAt: computedAt,
		Refreshing: refreshing,
		// A missing snapshot isn't "incomplete", it's "not built yet" —
		// that's what Refreshing communicates.
		Complete:   !hasSnap || complete,
		RetryAfter: retryAfter,
	})
}

func parseFilters(r *http.Request, origin concerts.Location) concerts.Filters {
	q := r.URL.Query()
	f := concerts.Filters{Genre: q.Get("genre"), Venue: q.Get("venue"), Origin: origin}
	if v := q.Get("date_from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			f.DateFrom = t
		}
	}
	if v := q.Get("date_to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			f.DateTo = t.Add(24*time.Hour - time.Second)
		}
	}
	switch q.Get("weekday") {
	case "weekday":
		f.Weekday = concerts.WeekdayWeekday
	case "weekend":
		f.Weekday = concerts.WeekdayWeekend
	default:
		f.Weekday = concerts.WeekdayAll
	}
	if v := q.Get("radius"); v != "" {
		if radius, err := strconv.Atoi(v); err == nil && radius > 0 {
			f.RadiusMiles = radius
		}
	}
	return f
}

// computeFacets counts *events*, not concerts. The list renders one card
// per event, so a genre pill has to count the cards that clicking it
// leaves behind: a festival where the user matched six rock acts is one
// rock card, not six. Each bucket therefore collects distinct event keys
// and reports how many it holds.
func computeFacets(cs []concerts.Concert) facetSet {
	genreEvents := map[string]map[string]struct{}{}
	// Venues are bucketed by their normalized form — the same key
	// concerts.Apply filters on — so a room that two sources spell
	// differently ("9:30 CLUB" / "9:30 Club") is one facet with one honest
	// count. spellings tracks the raw variants so we can show a real name
	// rather than the normalized slug.
	venueEvents := map[string]map[string]struct{}{}
	spellings := map[string]map[string]int{}
	for _, c := range cs {
		ek := concerts.EventKey(c.Date, c.Venue, c.City)
		seen := map[string]bool{}
		for _, g := range c.Artist.Genres {
			if seen[g] {
				continue
			}
			seen[g] = true
			addEvent(genreEvents, g, ek)
		}
		if raw := strings.TrimSpace(c.Venue); raw != "" {
			key := concerts.Normalize(raw)
			addEvent(venueEvents, key, ek)
			if spellings[key] == nil {
				spellings[key] = map[string]int{}
			}
			spellings[key][raw]++
		}
	}

	genres := make([]facet, 0, len(genreEvents))
	for g, evs := range genreEvents {
		genres = append(genres, facet{Value: g, Count: len(evs)})
	}
	sortFacets(genres)

	venues := make([]facet, 0, len(venueEvents))
	for key, evs := range venueEvents {
		venues = append(venues, facet{Value: pickSpelling(spellings[key]), Count: len(evs)})
	}
	sortFacets(venues)

	return facetSet{Genres: genres, Venues: venues}
}

func addEvent(buckets map[string]map[string]struct{}, bucket, eventKey string) {
	if buckets[bucket] == nil {
		buckets[bucket] = map[string]struct{}{}
	}
	buckets[bucket][eventKey] = struct{}{}
}

// sortFacets orders by descending count, then value, so the list is stable
// across requests with identical data.
func sortFacets(f []facet) {
	sort.Slice(f, func(i, j int) bool {
		if f[i].Count != f[j].Count {
			return f[i].Count > f[j].Count
		}
		return f[i].Value < f[j].Value
	})
}

// pickSpelling chooses the display name for a venue bucket: the most common
// raw spelling, ties broken alphabetically so the label doesn't flicker
// between requests.
func pickSpelling(variants map[string]int) string {
	best, bestN := "", -1
	for raw, n := range variants {
		if n > bestN || (n == bestN && raw < best) {
			best, bestN = raw, n
		}
	}
	return best
}
