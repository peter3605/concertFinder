package http

import (
	"log/slog"
	"net/http"
	"sort"
	"strconv"
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
	Location   concerts.Location  `json:"location"`
	Count      int                `json:"count"`
	Concerts   []concerts.Concert `json:"concerts"`
	Facets     facetSet           `json:"facets"`
	ComputedAt *time.Time         `json:"computed_at,omitempty"`
	Refreshing bool               `json:"refreshing"`
}

type facetSet struct {
	Genres []facet `json:"genres"`
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

	loc := h.FallbackLocation
	if userLoc, hit, err := db.GetUserLocation(r.Context(), h.Pool, u.ID); err != nil {
		slog.Warn("concerts: user location lookup failed", "err", err, "user", u.ID)
	} else if hit {
		loc = concerts.Location{
			Latitude:    userLoc.Latitude,
			Longitude:   userLoc.Longitude,
			RadiusMiles: userLoc.RadiusMiles,
		}
	}
	locKey := jobs.LocationKey(loc)

	var (
		found      []concerts.Concert
		computedAt *time.Time
		hasSnap    bool
	)
	if snap, hit, err := db.GetConcertSnapshot(r.Context(), h.Pool, u.ID, locKey); err != nil {
		slog.Warn("concerts: snapshot read failed", "err", err, "user", u.ID)
	} else if hit {
		hasSnap = true
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

	// SWR: enqueue a background scan when the snapshot is missing or older
	// than the staleness window. River's uniqueness guarantees we don't pile
	// up duplicate jobs if the user refreshes rapidly.
	stale := !hasSnap || (computedAt != nil && time.Since(*computedAt) > h.SnapshotStaleAfter)
	refreshing := false
	if stale && h.River != nil {
		args := jobs.ScanConcertsArgs{
			UserID:      u.ID,
			Latitude:    loc.Latitude,
			Longitude:   loc.Longitude,
			RadiusMiles: loc.RadiusMiles,
		}
		opts := &river.InsertOpts{
			UniqueOpts: river.UniqueOpts{
				ByArgs:   true,
				ByPeriod: 30 * time.Second, // collapse a burst of refreshes
			},
		}
		if _, err := h.River.Insert(r.Context(), args, opts); err != nil {
			slog.Warn("concerts: scan enqueue failed", "err", err, "user", u.ID)
		} else {
			refreshing = true
		}
	}

	facets := computeFacets(found)
	filters := parseFilters(r, loc)
	filtered := concerts.Apply(found, filters)

	// Overlay per-user saved + subscribed status. Both sets are small
	// (bounded by user's own picks) so a single read + O(n) tag is fine.
	saved, err := db.GetSavedDedupKeys(r.Context(), h.Pool, u.ID)
	if err != nil {
		slog.Warn("concerts: saved lookup failed", "err", err, "user", u.ID)
	}
	subscribed, err := db.GetSubscribedArtistIDs(r.Context(), h.Pool, u.ID)
	if err != nil {
		slog.Warn("concerts: subscribed lookup failed", "err", err, "user", u.ID)
	}
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

	writeJSON(w, concertsResponse{
		Location:   loc,
		Count:      len(filtered),
		Concerts:   filtered,
		Facets:     facets,
		ComputedAt: computedAt,
		Refreshing: refreshing,
	})
}

func parseFilters(r *http.Request, origin concerts.Location) concerts.Filters {
	q := r.URL.Query()
	f := concerts.Filters{Genre: q.Get("genre"), Origin: origin}
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

func computeFacets(cs []concerts.Concert) facetSet {
	genreCounts := map[string]int{}
	for _, c := range cs {
		seen := map[string]bool{}
		for _, g := range c.Artist.Genres {
			if seen[g] {
				continue
			}
			seen[g] = true
			genreCounts[g]++
		}
	}
	genres := make([]facet, 0, len(genreCounts))
	for g, n := range genreCounts {
		genres = append(genres, facet{Value: g, Count: n})
	}
	sort.Slice(genres, func(i, j int) bool {
		if genres[i].Count != genres[j].Count {
			return genres[i].Count > genres[j].Count
		}
		return genres[i].Value < genres[j].Value
	})
	return facetSet{Genres: genres}
}
