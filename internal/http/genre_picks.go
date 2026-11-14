package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/picks"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// GenrePicksHandler serves a user's genre picks and the catalog they choose
// from (CF-31, docs/public-path.md §1.2 and §5).
//
// The catalog is one Ticketmaster call a day for the whole process, charged
// to whichever caller found it stale, through the same ledger as the artist
// picker.
type GenrePicksHandler struct {
	Pool    *pgxpool.Pool
	Catalog *picks.GenreCatalog
	Ledger  *rate.Ledger // nil = no enforcement (tests)
}

type genrePicksResponse struct {
	Picks []db.GenrePick `json:"picks"`
	// Max is the per-account limit, so a client can say "2 of 5".
	Max int `json:"max"`
}

type genreCatalogResponse struct {
	Genres []ticketmaster.Genre `json:"genres"`
}

type addGenrePickRequest struct {
	GenreID string `json:"genre_id"`
}

// List handles GET /me/genres. Database only; it never calls Ticketmaster.
func (h *GenrePicksHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ps, err := db.ListGenrePicks(r.Context(), h.Pool, u.ID)
	if err != nil {
		slog.Error("list genre picks failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, genrePicksResponse{Picks: ps, Max: db.MaxGenrePicks})
}

// Available handles GET /me/genres/available: the top-level Music genres.
func (h *GenrePicksHandler) Available(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	genres, err := h.Catalog.Genres(r.Context(), func() bool { return h.charge(r, u) })
	switch {
	case err == nil:
	case errors.Is(err, picks.ErrQuota):
		writeQuotaRefusal(w)
		return
	default:
		slog.Warn("genre catalog failed", "err", err, "user", u.ID)
		http.Error(w, "Ticketmaster is unavailable right now", http.StatusBadGateway)
		return
	}
	writeJSON(w, genreCatalogResponse{Genres: genres})
}

// Add handles POST /me/genres with {"genre_id": "..."}. Only the ID is
// accepted, and only one in the catalog; the stored name is Ticketmaster's.
func (h *GenrePicksHandler) Add(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req addGenrePickRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(req.GenreID)
	if id == "" || len(id) > maxArtistIDLen {
		http.Error(w, "genre_id required", http.StatusBadRequest)
		return
	}
	g, err := h.Catalog.Lookup(r.Context(), id, func() bool { return h.charge(r, u) })
	switch {
	case err == nil:
	case errors.Is(err, picks.ErrQuota):
		writeQuotaRefusal(w)
		return
	case errors.Is(err, picks.ErrUnknownGenre):
		// Sub-genres land here too, deliberately: too sparse to be a feed.
		http.Error(w, "not a top-level music genre", http.StatusNotFound)
		return
	default:
		slog.Warn("genre lookup failed", "err", err, "user", u.ID, "genre", id)
		http.Error(w, "Ticketmaster is unavailable right now", http.StatusBadGateway)
		return
	}
	pick := db.GenrePick{GenreID: g.ID, Name: g.Name}
	added, err := db.AddGenrePick(r.Context(), h.Pool, u.ID, pick)
	if err != nil {
		slog.Error("add genre pick failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !added {
		http.Error(w, "you have picked the maximum number of genres; remove one to add another", http.StatusConflict)
		return
	}
	writeJSONStatus(w, http.StatusCreated, pick)
}

// Delete handles DELETE /me/genres/{genreID}.
func (h *GenrePicksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "genreID"))
	if id == "" || len(id) > maxArtistIDLen {
		http.Error(w, "invalid genre id", http.StatusBadRequest)
		return
	}
	if err := db.RemoveGenrePick(r.Context(), h.Pool, u.ID, id); err != nil {
		slog.Error("remove genre pick failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// charge spends one Ticketmaster permit for the caller; see
// ArtistPicksHandler.charge.
func (h *GenrePicksHandler) charge(r *http.Request, u auth.CurrentUser) bool {
	if h.Ledger == nil {
		return true
	}
	ok, err := h.Ledger.CheckAndIncrement(r.Context(), u.ID, rate.SourceTicketmaster)
	if err != nil {
		slog.Warn("genre catalog: ledger error, allowing", "err", err, "user", u.ID)
	}
	return ok
}
