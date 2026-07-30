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
)

// SavedConcertsHandler serves POST/DELETE /me/saved-concerts.
// Reads happen through GET /me/concerts (saved flag on each row) rather than
// through a dedicated list endpoint — the client's already fetching concerts
// and the frontend does the filtering, so a separate list would duplicate.
type SavedConcertsHandler struct {
	Pool *pgxpool.Pool
}

type saveRequest struct {
	DedupKey string `json:"dedup_key"`
}

func (h *SavedConcertsHandler) Post(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req saveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.DedupKey = strings.TrimSpace(req.DedupKey)
	if req.DedupKey == "" {
		http.Error(w, "dedup_key required", http.StatusBadRequest)
		return
	}
	if err := db.SaveConcert(r.Context(), h.Pool, u.ID, req.DedupKey); err != nil {
		slog.Error("save concert failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SavedConcertsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	key := chi.URLParam(r, "dedupKey")
	if key == "" {
		http.Error(w, "dedup_key required", http.StatusBadRequest)
		return
	}
	if err := db.UnsaveConcert(r.Context(), h.Pool, u.ID, key); err != nil {
		slog.Error("unsave concert failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
