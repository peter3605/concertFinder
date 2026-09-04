package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
)

// AdminHandler is the operator's console: mint an invite code, see what state
// every code is in, revoke one.
//
// It is deliberately WEB-ONLY and deliberately invisible to the API contract.
// Nothing here appears in /api/me/* or /api/auth/me, and no response anywhere
// tells a client whether its user is an admin. Two reasons, and the second is
// the load-bearing one:
//
//   - /api/me/* is consumed by iOS builds already on people's phones, so every
//     field in it is additive-only forever. An admin surface has no business
//     taking out that kind of mortgage.
//   - The web console does not need to be told. It calls GET /admin/invites
//     and renders itself on 200 or "not yours" on 403, which is one request
//     either way and cannot disagree with the server about who is an admin.
//
// The cost is that /admin is reached by typing it rather than by clicking a
// nav link, which for a console with one user is the right trade.
type AdminHandler struct {
	Pool *pgxpool.Pool
}

// Mount registers every admin route, and installs the admin gate itself.
//
// The r.Use is inside this function on purpose. CLAUDE.md's rule is to mount
// the gate at the chi Route level so a route added later inherits it instead
// of having to remember it; putting the r.Use here goes one step further, in
// that there is no call site that could register an admin route outside the
// gate even by accident. What is behind it is an invite mint -- an ungated one
// is an unbounded signup path, which is precisely what the invite gate exists
// to prevent -- so "somebody forgets" is not an acceptable failure mode.
//
// The caller still supplies RequireUser and CSRF, because those belong to the
// whole /api tree's policy rather than to this handler. RequireAdmin cannot
// substitute for RequireUser: it reads the user RequireUser resolved, and
// fails closed when there is none.
//
// mintLimit must be a limiter constructed once for the process. Passed in
// rather than built here for the same reason every other limiter is: one
// created inside the handler is a fresh empty bucket per request, i.e. no
// limiter at all.
func (h *AdminHandler) Mount(r chi.Router, mintLimit *auth.UserRateLimit) {
	r.Use(auth.RequireAdmin)
	// no-store rather than no-cache. These payloads are live invite codes and
	// operator notes; a shared cache holding them, or a browser restoring one
	// from bfcache after the flag is revoked, are both worse than a refetch.
	r.Use(noStore)
	r.Get("/invites", h.ListInvites)
	// Rate-limited because it writes a row per call. Every other reachable
	// mutation on this server has its own bucket, and "it is behind auth" is
	// not a bound -- an admin account with a stuck script is exactly how a
	// table fills up. Per user rather than per IP because the caller is always
	// authenticated by the time this runs.
	r.With(mintLimit.Middleware).Post("/invites", h.MintInvite)
	// POST .../disable, not DELETE .../{code}. db.DisableInviteCode revokes
	// without deleting -- a spent code has to stay readable to explain where a
	// user came from -- and DELETE would promise a removal that does not
	// happen.
	r.Post("/invites/{code}/disable", h.DisableInvite)
}

// noStore keeps admin payloads out of every cache between here and the screen.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// adminInvite is the wire shape of one code.
//
// State is computed server-side from db.InviteCode.State rather than left to
// the client to derive from the other four fields. The predicate for "usable"
// lives in SQL, is mirrored once in Go, and is rendered by both the CLI and
// this console; a third copy in TypeScript is how a code the console calls
// usable gets refused at redemption.
type adminInvite struct {
	Code           string  `json:"code"`
	Note           string  `json:"note"`
	MaxRedemptions int     `json:"max_redemptions"`
	Redemptions    int     `json:"redemptions"`
	State          string  `json:"state"`
	ExpiresAt      *string `json:"expires_at"`
	DisabledAt     *string `json:"disabled_at"`
	CreatedAt      string  `json:"created_at"`
}

func toAdminInvite(c db.InviteCode, now time.Time) adminInvite {
	out := adminInvite{
		Code:           c.Code,
		Note:           c.Note,
		MaxRedemptions: c.MaxRedemptions,
		Redemptions:    c.Redemptions,
		State:          c.State(now),
		CreatedAt:      c.CreatedAt.UTC().Format(time.RFC3339),
	}
	if c.ExpiresAt != nil {
		s := c.ExpiresAt.UTC().Format(time.RFC3339)
		out.ExpiresAt = &s
	}
	if c.DisabledAt != nil {
		s := c.DisabledAt.UTC().Format(time.RFC3339)
		out.DisabledAt = &s
	}
	return out
}

// ListInvites answers GET /api/admin/invites.
func (h *AdminHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	codes, err := db.ListInviteCodes(r.Context(), h.Pool)
	if err != nil {
		slog.Error("admin: list invites failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	out := make([]adminInvite, 0, len(codes))
	for _, c := range codes {
		out = append(out, toAdminInvite(c, now))
	}
	// A named object rather than a bare array, so a later field (a count, a
	// cursor) is an addition instead of a breaking change of shape.
	writeJSON(w, map[string]any{"invites": out})
}

type mintInviteRequest struct {
	Note string `json:"note"`
	// Uses is a pointer so "not sent" and "sent as 0" are distinguishable.
	// Omitted means one, which is the ordinary case and matches what
	// -mint-invite does with no flags; an explicit 0 is a mistake and is
	// refused rather than quietly turned into 1.
	Uses        *int `json:"uses"`
	ExpiresDays int  `json:"expires_days"`
}

// maxInviteNoteLen bounds the operator's own note. It is free text on a row
// anyone with the flag can write, so it is bounded for the same reason
// maxDisplayNameLen is: without a ceiling it is a kilobyte-per-row store.
const maxInviteNoteLen = 200

// maxInviteUses caps a single code's seats. A code for a household or a group
// of testers is the reason max_redemptions exists at all; a code for four
// thousand people is somebody turning the invite gate off through the gate.
const maxInviteUses = 50

// maxInviteExpiryDays is a little over a year. Longer than any invite should
// live, short enough that a fat-fingered number cannot overflow the date math.
const maxInviteExpiryDays = 400

// MintInvite answers POST /api/admin/invites.
func (h *AdminHandler) MintInvite(w http.ResponseWriter, r *http.Request) {
	var req mintInviteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	uses := 1
	if req.Uses != nil {
		uses = *req.Uses
	}
	if uses < 1 || uses > maxInviteUses {
		http.Error(w, "uses must be between 1 and 50", http.StatusBadRequest)
		return
	}
	if req.ExpiresDays < 0 || req.ExpiresDays > maxInviteExpiryDays {
		http.Error(w, "expires_days must be between 0 and 400", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > maxInviteNoteLen {
		http.Error(w, "note is too long", http.StatusBadRequest)
		return
	}
	var expiresAt *time.Time
	if req.ExpiresDays > 0 {
		t := time.Now().UTC().AddDate(0, 0, req.ExpiresDays)
		expiresAt = &t
	}
	// db.NewInviteCode and db.CreateInviteCode, not a second minting path.
	// The generator and the normalizer live together in internal/db because
	// they drifted once when they did not, and a code that does not match the
	// row it names presents to its holder as a broken invite.
	code, err := db.NewInviteCode()
	if err != nil {
		slog.Error("admin: generate invite failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	created, err := db.CreateInviteCode(r.Context(), h.Pool, code, note, uses, expiresAt)
	if err != nil {
		slog.Error("admin: mint invite failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSONStatus(w, http.StatusCreated, toAdminInvite(created, time.Now()))
}

// DisableInvite answers POST /api/admin/invites/{code}/disable.
func (h *AdminHandler) DisableInvite(w http.ResponseWriter, r *http.Request) {
	// Normalized by db.DisableInviteCode, which is the only normalizer. The
	// length check here is a bound on the path parameter, not a second
	// spelling of the format.
	code := chi.URLParam(r, "code")
	if code == "" || len(code) > maxDedupKeyLen {
		http.Error(w, "invalid code", http.StatusBadRequest)
		return
	}
	if err := db.DisableInviteCode(r.Context(), h.Pool, code); err != nil {
		if errors.Is(err, db.ErrNoRows) {
			http.Error(w, "no such code", http.StatusNotFound)
			return
		}
		slog.Error("admin: disable invite failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
