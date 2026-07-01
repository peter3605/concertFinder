package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/bandsintown"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// concertsRequestBudget bounds the outer /me/concerts request per design §6.1.
const concertsRequestBudget = 15 * time.Second

// ConcertsHandler serves /me/concerts.
type ConcertsHandler struct {
	Affinity *AffinityService
	Pool     *pgxpool.Pool
	TM       *ticketmaster.Client
	BIT      *bandsintown.Client
	Location concerts.Location
}

type concertsResponse struct {
	Location concerts.Location  `json:"location"`
	Count    int                `json:"count"`
	Concerts []concerts.Concert `json:"concerts"`
}

func (h *ConcertsHandler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), concertsRequestBudget)
	defer cancel()

	artists, _, _, err := h.Affinity.LoadOrCompute(ctx, u)
	if err != nil {
		slog.Error("concerts: affinity failed", "err", err, "user", u.ID)
		http.Error(w, "affinity load failed", http.StatusInternalServerError)
		return
	}

	found, err := concerts.Search(ctx, concerts.SearchDeps{
		Pool:        h.Pool,
		TM:          h.TM,
		BIT:         h.BIT,
		CacheTTL:    4 * time.Hour,
		Parallelism: 10,
	}, artists, h.Location)
	if err != nil {
		slog.Error("concerts: search failed", "err", err, "user", u.ID)
		http.Error(w, "search failed", http.StatusBadGateway)
		return
	}

	writeJSON(w, concertsResponse{Location: h.Location, Count: len(found), Concerts: found})
}
