package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/spotify"
)

// AffinityHandler serves the load-or-compute affinity profile.
type AffinityHandler struct {
	Service *affinity.Service
}

type affinityResponse struct {
	ComputedAt time.Time              `json:"computed_at"`
	Cached     bool                   `json:"cached"`
	Artists    []spotify.ScoredArtist `json:"artists"`
}

func (h *AffinityHandler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	artists, computedAt, cached, err := h.Service.LoadOrCompute(r.Context(), affinity.User{ID: u.ID, SpotifyUserID: u.SpotifyUserID})
	if err != nil {
		slog.Error("affinity load-or-compute failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, affinityResponse{ComputedAt: computedAt, Cached: cached, Artists: artists})
}
