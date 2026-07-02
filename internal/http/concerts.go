package http

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/bandsintown"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/search"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// initialResponseBudget bounds how long a fresh /me/concerts call waits
// before returning partial results (design §6.1: 15s outer deadline).
const initialResponseBudget = 15 * time.Second

// pollResponseBudget is a shorter wait when the client is polling a running
// search — enough for quick progress but not enough to block a busy client.
const pollResponseBudget = 5 * time.Second

// ConcertsHandler serves /me/concerts.
type ConcertsHandler struct {
	Affinity         *affinity.Service
	Pool             *pgxpool.Pool
	TM               *ticketmaster.Client
	BIT              *bandsintown.Client
	FallbackLocation concerts.Location
	Fallback         concerts.Fallbacker
	MinFallbackScore float64
	Searches         *search.Manager
}

type concertsResponse struct {
	SearchID uuid.UUID          `json:"search_id"`
	Complete bool               `json:"complete"`
	Location concerts.Location  `json:"location"`
	Count    int                `json:"count"`
	Concerts []concerts.Concert `json:"concerts"`
	Facets   facetSet           `json:"facets"`
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

	// Polling an existing search?
	if raw := r.URL.Query().Get("id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		s, ok := h.Searches.Get(id)
		if !ok {
			http.Error(w, "search not found or expired", http.StatusNotFound)
			return
		}
		h.respondFromSearch(w, r, s, loc, pollResponseBudget)
		return
	}

	// Fresh search: compute affinity synchronously (cheap on cache hit), then
	// hand the top-N to a detached fan-out.
	artists, _, _, err := h.Affinity.LoadOrCompute(r.Context(), affinity.User{ID: u.ID, SpotifyUserID: u.SpotifyUserID})
	if err != nil {
		slog.Error("concerts: affinity failed", "err", err, "user", u.ID)
		http.Error(w, "affinity load failed", http.StatusInternalServerError)
		return
	}
	s := h.Searches.Start(concerts.SearchDeps{
		Pool:             h.Pool,
		TM:               h.TM,
		BIT:              h.BIT,
		CacheTTL:         4 * time.Hour,
		Parallelism:      10,
		Fallback:         h.Fallback,
		MinFallbackScore: h.MinFallbackScore,
	}, artists, loc)
	h.respondFromSearch(w, r, s, loc, initialResponseBudget)
}

// respondFromSearch waits up to budget for completion, snapshots the merger,
// applies filters/facets, and writes the response.
func (h *ConcertsHandler) respondFromSearch(w http.ResponseWriter, r *http.Request, s *search.Search, loc concerts.Location, budget time.Duration) {
	waitCtx, cancel := waitContext(r.Context(), budget)
	defer cancel()
	s.WaitFor(waitCtx)

	found := s.Merger.All()
	facets := computeFacets(found)
	filters := parseFilters(r, loc)
	filtered := concerts.Apply(found, filters)

	writeJSON(w, concertsResponse{
		SearchID: s.ID,
		Complete: s.IsComplete(),
		Location: loc,
		Count:    len(filtered),
		Concerts: filtered,
		Facets:   facets,
	})
}

func waitContext(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
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
