package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
)

// SavedConcertsHandler serves GET/POST/DELETE /me/saved-concerts.
//
// The list is read from user_saved_concerts joined to the shared concerts
// table, NOT by filtering the user's snapshot. Reading it out of the
// snapshot (the old `GET /me/concerts?saved_only=true` route) meant a save
// only survived while its artist stayed in the affinity top-200; the next
// recompute could drop the artist and the user's saved show would disappear
// with no message and no way to get it back.
type SavedConcertsHandler struct {
	Pool *pgxpool.Pool
	// FallbackLocation is echoed back when the user has no saved location,
	// so the response shape matches /me/concerts.
	FallbackLocation concerts.Location
}

// List returns the user's saved concerts, newest event first, with the same
// response shape as /me/concerts so the frontend can reuse its renderer.
func (h *SavedConcertsHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	blobs, err := db.GetSavedConcerts(r.Context(), h.Pool, u.ID)
	if err != nil {
		slog.Error("saved concerts: load failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	saved, err := concerts.Assemble(blobs)
	if err != nil {
		slog.Error("saved concerts: assemble failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Everything here is saved by definition; tag it so the star renders
	// filled without the client having to infer it.
	for i := range saved {
		saved[i].Saved = true
	}
	subscribed, err := db.GetSubscribedArtistIDs(r.Context(), h.Pool, u.ID)
	if err != nil {
		slog.Warn("saved concerts: subscribed lookup failed", "err", err, "user", u.ID)
	}
	for i := range saved {
		if _, ok := subscribed[saved[i].Artist.ID]; ok {
			saved[i].Subscribed = true
		}
	}

	loc := h.FallbackLocation
	if userLoc, hit, err := db.GetUserLocation(r.Context(), h.Pool, u.ID); err == nil && hit {
		loc = concerts.Location{
			Latitude:    userLoc.Latitude,
			Longitude:   userLoc.Longitude,
			RadiusMiles: userLoc.RadiusMiles,
		}
	}

	// Same past-show floor as /me/concerts, and for the same reason: the
	// saved query orders by event_date ascending, so shows that have already
	// happened sorted to the very top of the page.
	saved = concerts.Apply(saved, concerts.Filters{DateFrom: startOfUTCDay(time.Now())})
	facets := computeFacets(saved)
	filtered := concerts.Apply(saved, parseFilters(r))
	// Grouped the same way as /me/concerts. Saving two acts on one bill
	// yields one card here too, with both stars filled — otherwise the same
	// festival looks like six separate saves the user has to unsave one by
	// one.
	events := concerts.GroupEvents(filtered)

	writeJSON(w, concertsResponse{
		Location: loc,
		Count:    len(events),
		Events:   events,
		Facets:   facets,
		// A saved list is never mid-refresh: it reflects the saves table
		// directly rather than a snapshot that a background scan rebuilds.
		Refreshing: false,
		Complete:   true,
	})
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.DedupKey = strings.TrimSpace(req.DedupKey)
	if req.DedupKey == "" {
		http.Error(w, "dedup_key required", http.StatusBadRequest)
		return
	}
	// The key is caller-supplied and goes straight into a primary key, so its
	// length is ours to bound. A real one is 64 hex characters.
	if len(req.DedupKey) > maxDedupKeyLen {
		http.Error(w, "dedup_key is too long", http.StatusBadRequest)
		return
	}
	saved, err := db.SaveConcert(r.Context(), h.Pool, u.ID, req.DedupKey, maxSavedConcerts)
	if err != nil {
		slog.Error("save concert failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !saved {
		// 409 rather than 429: waiting changes nothing, and the remedy is the
		// user's own — unsave something. Saying which limit was hit matters
		// because the alternative is a star that silently refuses to stick.
		http.Error(w, "you have reached the maximum number of saved shows; remove one to save another", http.StatusConflict)
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
	if len(key) > maxDedupKeyLen {
		http.Error(w, "dedup_key is too long", http.StatusBadRequest)
		return
	}
	if err := db.UnsaveConcert(r.Context(), h.Pool, u.ID, key); err != nil {
		slog.Error("unsave concert failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
