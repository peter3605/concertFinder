package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
)

// EmailPrefsHandler exposes PUT /me/email-prefs for the frontend settings UI.
// Read of the current prefs happens via /api/auth/me (already returns email +
// digest_opt_in), so no GET endpoint here.
type EmailPrefsHandler struct {
	Pool *pgxpool.Pool
}

type emailPrefsRequest struct {
	// Pointers so the client can PATCH-style update either flag independently:
	// nil = "don't touch"; false = "set false".
	DigestOptIn        *bool `json:"digest_opt_in,omitempty"`
	InstantNotifyOptIn *bool `json:"instant_notify_opt_in,omitempty"`
	// PushOptIn rides on this endpoint rather than a new one: it is the same
	// kind of preference and the settings screen writes all three together.
	// Unlike the email flags it has no address requirement — push needs a
	// registered device, which the client has already arranged by the time it
	// can offer the toggle.
	PushOptIn *bool `json:"push_opt_in,omitempty"`
}

func (h *EmailPrefsHandler) Put(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req emailPrefsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	full, ok := auth.FullUserFromContext(r.Context())
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Enforce: can't opt in to either *email* flavor without an address on
	// file. Push is deliberately outside this check — it needs a device, not
	// an address, and requiring one would make "push me but do not email me"
	// unexpressible.
	wantsAnyOn := (req.DigestOptIn != nil && *req.DigestOptIn) || (req.InstantNotifyOptIn != nil && *req.InstantNotifyOptIn)
	if wantsAnyOn && full.Email == "" {
		http.Error(w, "log in again to grant email access before opting in", http.StatusBadRequest)
		return
	}
	if req.DigestOptIn != nil {
		if err := db.SetDigestOptIn(r.Context(), h.Pool, u.ID, *req.DigestOptIn); err != nil {
			slog.Error("digest prefs update failed", "err", err, "user", u.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	if req.InstantNotifyOptIn != nil {
		if err := db.SetInstantNotifyOptIn(r.Context(), h.Pool, u.ID, *req.InstantNotifyOptIn); err != nil {
			slog.Error("instant notify prefs update failed", "err", err, "user", u.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	if req.PushOptIn != nil {
		if err := db.SetPushOptIn(r.Context(), h.Pool, u.ID, *req.PushOptIn); err != nil {
			slog.Error("push prefs update failed", "err", err, "user", u.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
