package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
)

// AccountHandler exposes account-lifecycle endpoints. Currently just
// DELETE /me/account which cascades a user removal through every table
// that has an ON DELETE CASCADE foreign key to users. Rows in shared
// caches (concerts, mb_url_cache, venue_geo_cache) are left alone — they
// contain no user identity and other users may still benefit from them.
type AccountHandler struct {
	Pool *pgxpool.Pool
	// Tokens, when set, has its in-memory access-token entry for the user
	// dropped on delete so nothing keeps working after the row is gone.
	Tokens *auth.TokenService
}

type deleteAccountRequest struct {
	// ConfirmName must equal the user's display_name (case-insensitive
	// trim). Prevents accidental one-click deletion.
	ConfirmName string `json:"confirm_name"`
}

func (h *AccountHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	full, ok := auth.FullUserFromContext(r.Context())
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var req deleteAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	want := strings.ToLower(strings.TrimSpace(full.DisplayName))
	got := strings.ToLower(strings.TrimSpace(req.ConfirmName))
	if want == "" || want != got {
		http.Error(w, "confirm_name must exactly match your display name", http.StatusBadRequest)
		return
	}
	// FKs on sessions, user_locations, affinity_profiles,
	// user_concert_snapshots, user_saved_concerts, user_subscribed_artists,
	// user_digest_sent, rate_ledger are all ON DELETE CASCADE — this one
	// DELETE takes everything with it.
	const q = `DELETE FROM users WHERE id = $1`
	if _, err := h.Pool.Exec(r.Context(), q, u.ID); err != nil {
		slog.Error("account delete failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if h.Tokens != nil {
		h.Tokens.Forget(u.ID)
	}
	slog.Info("account deleted", "user", u.ID)
	// Clear session cookie so the browser stops sending stale credentials.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
	})
	w.WriteHeader(http.StatusNoContent)
}
