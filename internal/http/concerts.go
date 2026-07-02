package http

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/bandsintown"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// concertsRequestBudget bounds the outer /me/concerts request per design §6.1.
const concertsRequestBudget = 15 * time.Second

// ConcertsHandler serves /me/concerts.
type ConcertsHandler struct {
	Affinity         *affinity.Service
	Pool             *pgxpool.Pool
	TM               *ticketmaster.Client
	BIT              *bandsintown.Client
	FallbackLocation concerts.Location // used if the user hasn't set one
	Fallback         concerts.Fallbacker
	MinFallbackScore float64
}

type concertsResponse struct {
	Location concerts.Location  `json:"location"`
	Count    int                `json:"count"`
	Concerts []concerts.Concert `json:"concerts"`
	// Facets carry choices computed from the unfiltered result set so the
	// frontend can render pills without a second request.
	Facets facetSet `json:"facets"`
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
	ctx, cancel := context.WithTimeout(r.Context(), concertsRequestBudget)
	defer cancel()

	loc := h.FallbackLocation
	if userLoc, hit, err := db.GetUserLocation(ctx, h.Pool, u.ID); err != nil {
		slog.Warn("concerts: user location lookup failed", "err", err, "user", u.ID)
	} else if hit {
		loc = concerts.Location{
			Latitude:    userLoc.Latitude,
			Longitude:   userLoc.Longitude,
			RadiusMiles: userLoc.RadiusMiles,
		}
	}

	artists, _, _, err := h.Affinity.LoadOrCompute(ctx, affinity.User{ID: u.ID, SpotifyUserID: u.SpotifyUserID})
	if err != nil {
		slog.Error("concerts: affinity failed", "err", err, "user", u.ID)
		http.Error(w, "affinity load failed", http.StatusInternalServerError)
		return
	}

	found, err := concerts.Search(ctx, concerts.SearchDeps{
		Pool:             h.Pool,
		TM:               h.TM,
		BIT:              h.BIT,
		CacheTTL:         4 * time.Hour,
		Parallelism:      10,
		Fallback:         h.Fallback,
		MinFallbackScore: h.MinFallbackScore,
	}, artists, loc)
	if err != nil {
		slog.Error("concerts: search failed", "err", err, "user", u.ID)
		http.Error(w, "search failed", http.StatusBadGateway)
		return
	}

	facets := computeFacets(found)
	filters := parseFilters(r, loc)
	filtered := concerts.Apply(found, filters)

	writeJSON(w, concertsResponse{Location: loc, Count: len(filtered), Concerts: filtered, Facets: facets})
}

// parseFilters reads /me/concerts query params. Invalid values fall back to
// defaults silently — filters are cosmetic and shouldn't 400 the request.
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
			// Include the whole day.
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
		if r, err := strconv.Atoi(v); err == nil && r > 0 {
			f.RadiusMiles = r
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
