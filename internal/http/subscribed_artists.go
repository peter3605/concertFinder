package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/spotify"
)

// SubscribedArtistsHandler serves the CRUD endpoints for the per-artist
// subscription list plus the Spotify search proxy that powers the picker UI.
type SubscribedArtistsHandler struct {
	Pool    *pgxpool.Pool
	Spotify *spotify.Client
	Tokens  *auth.TokenService
}

type subscribeRequest struct {
	// DisplayName is optional but strongly encouraged so the "manage
	// subscriptions" list can render names without a batch call back to
	// Spotify. Callers that already know the name (subscribe from a concert
	// row, subscribe from the search results) should always send it.
	DisplayName string `json:"display_name,omitempty"`
}

func (h *SubscribedArtistsHandler) Post(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	artistID := strings.TrimSpace(chi.URLParam(r, "artistID"))
	if artistID == "" {
		http.Error(w, "artist_id required", http.StatusBadRequest)
		return
	}
	var req subscribeRequest
	// Body is optional — if the caller omits it, decode fails silently.
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := db.SubscribeArtist(r.Context(), h.Pool, u.ID, artistID, strings.TrimSpace(req.DisplayName)); err != nil {
		slog.Error("subscribe artist failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SubscribedArtistsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	artistID := strings.TrimSpace(chi.URLParam(r, "artistID"))
	if artistID == "" {
		http.Error(w, "artist_id required", http.StatusBadRequest)
		return
	}
	if err := db.UnsubscribeArtist(r.Context(), h.Pool, u.ID, artistID); err != nil {
		slog.Error("unsubscribe artist failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// List returns the user's full subscribed list. Ordered by name.
func (h *SubscribedArtistsHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rows, err := db.ListSubscribedArtists(r.Context(), h.Pool, u.ID)
	if err != nil {
		slog.Error("list subscribed failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Reshape to a public JSON shape without leaking DB struct tags.
	out := make([]map[string]string, 0, len(rows))
	for _, a := range rows {
		out = append(out, map[string]string{"id": a.SpotifyArtistID, "name": a.DisplayName})
	}
	writeJSON(w, map[string]any{"artists": out})
}

// SearchArtists proxies to Spotify's /v1/search?type=artist using the
// caller's own access token (refreshed on demand). Frontend debounces the
// input so this doesn't fire on every keystroke.
func (h *SubscribedArtistsHandler) SearchArtists(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, map[string]any{"artists": []any{}})
		return
	}
	token, err := h.Tokens.AccessTokenFor(r.Context(), u.ID)
	if err != nil {
		slog.Error("search artists: token failed", "err", err, "user", u.ID)
		http.Error(w, "spotify token error", http.StatusBadGateway)
		return
	}
	results, err := h.Spotify.SearchArtists(r.Context(), token, query, 10)
	if err != nil {
		slog.Warn("search artists: spotify error", "err", err, "user", u.ID)
		http.Error(w, "spotify search failed", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"artists": results})
}
