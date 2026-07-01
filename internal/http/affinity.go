package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/spotify"
)

// AffinityHandler serves the load-or-compute affinity profile.
type AffinityHandler struct {
	Pool    *pgxpool.Pool
	Tokens  *auth.TokenService
	Spotify *spotify.Client
	TTL     time.Duration // profile freshness window; design §7.3 = 24h
}

const affinityComputeTimeout = 60 * time.Second

type affinityResponse struct {
	ComputedAt time.Time               `json:"computed_at"`
	Cached     bool                    `json:"cached"`
	Artists    []spotify.ScoredArtist  `json:"artists"`
}

// Get returns the caller's affinity profile, computing if none is fresh.
func (h *AffinityHandler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if blob, computedAt, ok, err := db.LoadFreshAffinityProfile(r.Context(), h.Pool, u.ID, h.TTL); err != nil {
		slog.Error("load affinity failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if ok {
		var artists []spotify.ScoredArtist
		if err := json.Unmarshal(blob, &artists); err != nil {
			slog.Error("decode affinity blob failed", "err", err, "user", u.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, affinityResponse{ComputedAt: computedAt, Cached: true, Artists: artists})
		return
	}

	// Cache miss: hydrate + score. Bound compute time server-side.
	ctx, cancel := context.WithTimeout(r.Context(), affinityComputeTimeout)
	defer cancel()

	accessToken, err := h.Tokens.AccessTokenFor(ctx, u.ID)
	if err != nil {
		slog.Error("access token failed", "err", err, "user", u.ID)
		http.Error(w, "spotify auth failed", http.StatusBadGateway)
		return
	}
	sources, err := h.Spotify.HydrateSources(ctx, accessToken, u.SpotifyUserID)
	if err != nil {
		slog.Error("hydrate sources failed", "err", err, "user", u.ID)
		http.Error(w, "spotify fetch failed", http.StatusBadGateway)
		return
	}
	artists := spotify.ScoreArtists(sources)

	blob, err := json.Marshal(artists)
	if err != nil {
		slog.Error("marshal affinity failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := db.SaveAffinityProfile(r.Context(), h.Pool, u.ID, blob); err != nil {
		// Non-fatal for the response — log and continue.
		slog.Error("save affinity failed", "err", err, "user", u.ID)
	}

	writeJSON(w, affinityResponse{ComputedAt: time.Now(), Cached: false, Artists: artists})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
