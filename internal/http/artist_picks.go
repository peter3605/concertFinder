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

// ArtistPicksHandler serves the hand-picked artist list and the Ticketmaster
// picker that fills it (CF-30, docs/public-path.md §1.1 and §5).
//
// Every Ticketmaster call made here is charged to the caller through
// Ledger.CheckAndIncrement, which also charges the account-wide ceiling. The
// picker is the onboarding cost in docs/public-path.md §2.2 (about 50 calls
// per signup); an uncounted path to the upstream beside the counted ones is
// how signed-in users' scans end up as 403s that look like artists with no
// shows.
type ArtistPicksHandler struct {
	Pool   *pgxpool.Pool
	Picks  *picks.Service
	Ledger *rate.Ledger // nil = no enforcement (tests)
}

// maxPickQueryLen bounds a picker search. The longest artist name in the
// coverage measurement was well under this; the bound is about what reaches
// Ticketmaster and the cache, not about real names.
const maxPickQueryLen = 100

type picksListResponse struct {
	Picks []db.ArtistPick `json:"picks"`
	// Max is the per-account limit, so a client can say "12 of 30" without
	// hardcoding a number the server may change.
	Max int `json:"max"`
}

type pickSearchResponse struct {
	Query   string         `json:"query"`
	Results []picks.Result `json:"results"`
}

type addPickRequest struct {
	AttractionID string `json:"attraction_id"`
}

// List handles GET /me/picks.
func (h *ArtistPicksHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ps, err := db.ListArtistPicks(r.Context(), h.Pool, u.ID)
	if err != nil {
		slog.Error("list picks failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, picksListResponse{Picks: ps, Max: db.MaxArtistPicks})
}

// Search handles GET /me/picks/search?q=. One Ticketmaster call per submitted
// query, none for a query asked within picks.CacheTTL. Search on submit, not
// on keystroke: keyword search has no prefix match, so a partial name returns
// nothing at the price of a full call.
func (h *ArtistPicksHandler) Search(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "q required", http.StatusBadRequest)
		return
	}
	if len(q) > maxPickQueryLen {
		http.Error(w, "q is too long", http.StatusBadRequest)
		return
	}
	if res, ok := h.Picks.Cached(q); ok {
		writeJSON(w, pickSearchResponse{Query: q, Results: res})
		return
	}
	if !h.charge(r, u) {
		writeQuotaRefusal(w)
		return
	}
	res, err := h.Picks.Search(r.Context(), q)
	if err != nil {
		slog.Warn("pick search failed", "err", err, "user", u.ID)
		http.Error(w, "search is unavailable right now", http.StatusBadGateway)
		return
	}
	writeJSON(w, pickSearchResponse{Query: q, Results: res})
}

// Add handles POST /me/picks with {"attraction_id": "..."}.
//
// Only the ID is accepted. Name and genre come from Ticketmaster, through the
// picker's own recent results or one by-ID call -- see picks.Service.Lookup
// for why a client-supplied name cannot be trusted here.
func (h *ArtistPicksHandler) Add(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req addPickRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(req.AttractionID)
	if id == "" {
		http.Error(w, "attraction_id required", http.StatusBadRequest)
		return
	}
	if len(id) > maxArtistIDLen {
		http.Error(w, "attraction_id is too long", http.StatusBadRequest)
		return
	}
	res, _, err := h.Picks.Lookup(r.Context(), id, func() bool { return h.charge(r, u) })
	switch {
	case err == nil:
	case errors.Is(err, picks.ErrQuota):
		writeQuotaRefusal(w)
		return
	case errors.Is(err, ticketmaster.ErrAttractionNotFound):
		http.Error(w, "no such artist on Ticketmaster", http.StatusNotFound)
		return
	default:
		slog.Warn("pick lookup failed", "err", err, "user", u.ID, "attraction", id)
		http.Error(w, "Ticketmaster is unavailable right now", http.StatusBadGateway)
		return
	}
	pick := db.ArtistPick{AttractionID: res.AttractionID, Name: res.Name, Genre: res.Genre}
	added, err := db.AddArtistPick(r.Context(), h.Pool, u.ID, pick)
	if err != nil {
		slog.Error("add pick failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !added {
		// 409, as for subscriptions: waiting does not help, removing one does.
		http.Error(w, "you have picked the maximum number of artists; remove one to add another", http.StatusConflict)
		return
	}
	writeJSONStatus(w, http.StatusCreated, pick)
}

// Delete handles DELETE /me/picks/{attractionID}.
func (h *ArtistPicksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "attractionID"))
	if id == "" || len(id) > maxArtistIDLen {
		http.Error(w, "invalid attraction id", http.StatusBadRequest)
		return
	}
	if err := db.RemoveArtistPick(r.Context(), h.Pool, u.ID, id); err != nil {
		slog.Error("remove pick failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// charge spends one Ticketmaster permit for the caller. A ledger error fails
// open, as CheckAndIncrement documents: a database blip should not take the
// picker down.
func (h *ArtistPicksHandler) charge(r *http.Request, u auth.CurrentUser) bool {
	if h.Ledger == nil {
		return true
	}
	ok, err := h.Ledger.CheckAndIncrement(r.Context(), u.ID, rate.SourceTicketmaster)
	if err != nil {
		slog.Warn("pick search: ledger error, allowing", "err", err, "user", u.ID)
	}
	return ok
}

func writeQuotaRefusal(w http.ResponseWriter) {
	// Same remedy as a capped scan: the UTC day has to roll over.
	http.Error(w, "today's search allowance is used up; try again tomorrow", http.StatusTooManyRequests)
}
