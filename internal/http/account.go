package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
)

// AccountHandler exposes account-lifecycle endpoints. Two, and the difference
// between them matters:
//
//   - DELETE /me/account cascades a user removal through every table with an
//     ON DELETE CASCADE foreign key to users. Rows in shared caches (concerts,
//     mb_url_cache, venue_geo_cache) are left alone — they contain no user
//     identity and other users may still benefit from them. Irreversible.
//   - DELETE /me/spotify-connection revokes the Spotify credential and the
//     data derived from it, keeping the account, its saves and its
//     subscriptions. Recoverable by signing in again.
//
// Both clear the session cookie, because both end every session.
type AccountHandler struct {
	Pool *pgxpool.Pool
	// Tokens, when set, has its in-memory access-token entry for the user
	// dropped on delete so nothing keeps working after the row is gone.
	Tokens *auth.TokenService
	// CookieDomain must match what auth set the session cookie with. A
	// deletion Set-Cookie only matches a cookie with the same name, path AND
	// domain, so clearing without it left the original cookie in the browser
	// untouched whenever SESSION_COOKIE_DOMAIN was configured.
	CookieDomain string
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
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
	// Attributes must mirror auth.setSessionCookie exactly or the browser
	// treats this as a different cookie and keeps the original.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Domain:   h.CookieDomain,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// DisconnectSpotify severs the Spotify link without deleting the account.
//
// This is what App Store Guideline 5.1.1(v) asks for ("a mechanism to revoke
// social network credentials and disable data access between the app and
// social network from within the app"). Account deletion arguably satisfied
// the clause already, but it was the only option, and "delete everything" is a
// poor answer to a user who just wants us to stop holding their Spotify grant.
// See plan §10.1.2.
//
// Deliberately NOT gated on a confirm_name the way Delete is. That guard
// exists because account deletion is irreversible; this is recoverable by
// signing back in, and saves and subscriptions survive it. Requiring someone
// to type their display name to perform the *safer* of the two actions would
// teach them the confirmation is a formality.
func (h *AccountHandler) DisconnectSpotify(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := db.DisconnectSpotify(r.Context(), h.Pool, u.ID); err != nil {
		slog.Error("spotify disconnect failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The in-memory access token outlives the row it was minted from, so
	// without this a disconnected user keeps working until it expires — up to
	// an hour of calls against a grant they just revoked.
	if h.Tokens != nil {
		h.Tokens.Forget(u.ID)
	}
	slog.Info("spotify disconnected", "user", u.ID)
	// DisconnectSpotify deleted every session server-side; this clears the
	// browser's copy so it stops sending a credential that no longer resolves.
	// Attributes must mirror auth.setSessionCookie exactly or the browser
	// treats it as a different cookie and keeps the original.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Domain:   h.CookieDomain,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}
