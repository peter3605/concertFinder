package http

import (
	"log/slog"
	"net/http"
	"sort"
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

	if !validCoords(loc.Latitude, loc.Longitude) {
		// Defence in depth rather than a reachable state: PUT /me/location
		// validates what it stores and config.Validate rejects a bad
		// USER_LATITUDE/USER_LONGITUDE at startup. It is here because the
		// consequence of a bad pair reaching this point is not an error — it
		// is a valid-looking location_key, a snapshot filed under it, and a
		// five-minute scan job, all of which look completely normal from the
		// outside.
		slog.Error("concerts: refusing to scan an out-of-range location",
			"user", u.ID, "lat", loc.Latitude, "lng", loc.Longitude)
		http.Error(w, "your saved location is invalid; set it again", http.StatusInternalServerError)
		return
	}

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

	// Facets describe the set the user is choosing *within*, so they are
	// computed before the user's own filters are applied but after the
	// past-show floor — otherwise a pill would count cards that no longer
	// exist in any view of this list.
	found = concerts.Apply(found, concerts.Filters{DateFrom: startOfUTCDay(time.Now())})
	facets := computeFacets(found)
	filtered := concerts.Apply(found, parseFilters(r))

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

// ManualRefreshMinInterval is how recently a scan must have run before the
// refresh button refuses. A scan spends real upstream quota — the caps are
// sized at roughly one full scan's worth of calls per source per day — so
// without a floor a user holding down the button burns their own allowance
// and ends the day on a partial feed.
//
// River's uniqueness already prevents two scans running at once; this is
// about the gap *between* completed scans, which uniqueness says nothing
// about.
const ManualRefreshMinInterval = 15 * time.Minute

type refreshResponse struct {
	Refreshing bool `json:"refreshing"`
	// RetryAfter is when the button becomes usable again. Set on every
	// refusal so the UI can say when rather than just no.
	RetryAfter *time.Time `json:"retry_after,omitempty"`
	// Reason distinguishes "you just refreshed" from "your daily quota is
	// spent", which need different words in front of the user.
	Reason string `json:"reason,omitempty"`
}

// Refresh handles POST /me/concerts/refresh: an explicit user request to
// rescan now, rather than waiting for the SWR staleness window or the nightly
// fanout. Deliberately bypasses the staleness check — the whole point is to
// refresh a snapshot that is not yet stale — but not the quota guard.
func (h *ConcertsHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.River == nil {
		http.Error(w, "background jobs disabled", http.StatusServiceUnavailable)
		return
	}

	loc := h.FallbackLocation
	if userLoc, hit, err := db.GetUserLocation(r.Context(), h.Pool, u.ID); err != nil {
		slog.Warn("refresh: user location lookup failed", "err", err, "user", u.ID)
	} else if hit {
		loc = concerts.Location{
			Latitude:    userLoc.Latitude,
			Longitude:   userLoc.Longitude,
			RadiusMiles: userLoc.RadiusMiles,
		}
	}
	// Same guard as Get, and for the same reason: this path enqueues the very
	// job the coordinate identifies.
	if !validCoords(loc.Latitude, loc.Longitude) {
		slog.Error("refresh: refusing to scan an out-of-range location",
			"user", u.ID, "lat", loc.Latitude, "lng", loc.Longitude)
		http.Error(w, "your saved location is invalid; set it again", http.StatusInternalServerError)
		return
	}
	locKey := jobs.LocationKey(loc)

	var snapshot *db.ConcertSnapshot
	if snap, hit, err := db.GetConcertSnapshot(r.Context(), h.Pool, u.ID, locKey); err != nil {
		// Leave snapshot nil and let the scan run: failing to read the
		// snapshot is no reason to refuse a refresh the user asked for.
		slog.Warn("refresh: snapshot read failed", "err", err, "user", u.ID)
	} else if hit {
		snapshot = &snap
	}
	if retryAfter, reason := refreshRefusal(snapshot, time.Now()); reason != "" {
		writeRefreshRefusal(w, retryAfter, reason)
		return
	}

	args := jobs.ScanConcertsArgs{
		UserID:      u.ID,
		Latitude:    loc.Latitude,
		Longitude:   loc.Longitude,
		RadiusMiles: loc.RadiusMiles,
	}
	// Nil opts: uniqueness is declared on ScanConcertsArgs, and passing opts
	// here would override it. A click landing while a scan is already in
	// flight therefore dedupes into that scan and still reports refreshing —
	// which is the honest answer to "refresh now".
	if _, err := h.River.Insert(r.Context(), args, nil); err != nil {
		slog.Warn("refresh: scan enqueue failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, refreshResponse{Refreshing: true})
}

// refreshRefusal decides whether a manual refresh should be turned away,
// returning the instant it becomes allowed and a machine-readable reason.
// An empty reason means "go ahead".
//
// A nil snapshot — none exists, or the read failed — always proceeds: there
// is nothing to protect and the user is asking for the very thing that would
// create one.
func refreshRefusal(snap *db.ConcertSnapshot, now time.Time) (*time.Time, string) {
	if snap == nil {
		return nil, ""
	}
	// Quota exhaustion outranks recency. A capped scan cannot do better until
	// the rate ledger's UTC day rolls over, so letting the click through would
	// enqueue a job guaranteed to come back capped — and stamp another
	// complete=false snapshot on the way.
	if snap.RetryAfter != nil && now.Before(*snap.RetryAfter) {
		return snap.RetryAfter, "quota_exhausted"
	}
	if next := snap.ComputedAt.Add(ManualRefreshMinInterval); now.Before(next) {
		return &next, "too_soon"
	}
	return nil, ""
}

func writeRefreshRefusal(w http.ResponseWriter, retryAfter *time.Time, reason string) {
	writeJSONStatus(w, http.StatusTooManyRequests, refreshResponse{
		Refreshing: false,
		RetryAfter: retryAfter,
		Reason:     reason,
	})
}

func parseFilters(r *http.Request) concerts.Filters {
	q := r.URL.Query()
	f := concerts.Filters{Genre: q.Get("genre"), Venue: q.Get("venue")}
	// Absent an explicit lower bound, hide shows that have already happened.
	// Nothing else did: snapshots are rebuilt at most every few hours and the
	// janitor keeps past events for another 7 days, so last night's concert
	// sat at the top of a list headed "Upcoming concerts" until the next scan.
	// The floor is the start of the current UTC day rather than time.Now() so
	// a matinee that started a few hours ago still shows for the rest of its
	// day, and so the boundary doesn't depend on the server's timezone.
	f.DateFrom = startOfUTCDay(time.Now())
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
	return f
}

func startOfUTCDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
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
