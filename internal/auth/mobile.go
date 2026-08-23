package auth

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/peterho/concertfinder/internal/db"
)

// ClientIOS is the value of the ?client= query parameter that switches
// /api/auth/login into the app-initiated flow.
const ClientIOS = "ios"

// mobileExchangeRequest is the body of POST /api/auth/mobile/exchange.
type mobileExchangeRequest struct {
	Code     string `json:"code"`
	Verifier string `json:"verifier"`
}

// mobileExchangeResponse hands the app its session. SessionToken is the
// session ID — the same value the browser gets in cf_session — which the app
// stores in the Keychain and sends as Authorization: Bearer.
type mobileExchangeResponse struct {
	SessionToken string         `json:"session_token"`
	ExpiresAt    time.Time      `json:"expires_at"`
	User         map[string]any `json:"user"`
}

// handleMobileExchange redeems a one-time code for a session.
//
// The extra round trip exists so the session never rides in a URL. Callback
// URLs are logged by the OS, surface in ASWebAuthenticationSession
// diagnostics, and — with a custom scheme rather than a universal link — can
// be claimed by any other installed app. Binding the code to a challenge the
// app chose means intercepting the redirect is not enough: the interceptor
// would also need the verifier, which never leaves the device.
//
// Unauthenticated by design (the caller has no session yet — that is what it
// is asking for), and mounted under /api/auth so it inherits that subtree's
// IP rate limit.
func (d *Deps) handleMobileExchange(w http.ResponseWriter, r *http.Request) {
	var req mobileExchangeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Code == "" || req.Verifier == "" {
		writeAuthError(w, http.StatusBadRequest, "code and verifier are required")
		return
	}

	// Single-use is enforced by the DELETE ... RETURNING inside Take: a
	// replayed code loses to its own first redemption rather than both
	// succeeding.
	c, ok, err := db.TakeMobileAuthCode(r.Context(), d.Pool, req.Code)
	if err != nil {
		slog.Error("mobile exchange: take code failed", "err", err)
		writeAuthError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Unknown, already burned, and expired are one response on purpose.
	// Distinguishing them tells a caller which codes existed.
	if !ok {
		writeAuthError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	// The verifier must hash to the challenge recorded at /login. Constant
	// time because this is a secret comparison; the values are equal-length
	// base64 of a SHA-256, so length leakage is not a concern, but the
	// habit is worth keeping.
	got := ChallengeFromVerifier(req.Verifier)
	if subtle.ConstantTimeCompare([]byte(got), []byte(c.AppChallenge)) != 1 {
		slog.Warn("mobile exchange: verifier did not match challenge")
		// The code is already burned at this point. That is deliberate: a
		// wrong verifier is either a bug or an attack, and letting the code
		// survive for a retry would turn it into an oracle.
		writeAuthError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	su, err := db.GetSessionUser(r.Context(), d.Pool, c.SessionID)
	if err != nil {
		// The session was deleted between mint and redeem — logged out
		// elsewhere, or the account was deleted.
		writeAuthError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	writeJSONResponse(w, http.StatusOK, mobileExchangeResponse{
		SessionToken: su.Session.ID,
		ExpiresAt:    su.Session.ExpiresAt,
		User:         userPayload(su.User),
	})
}

// mintMobileCode creates the one-time code the callback redirects with.
func (d *Deps) mintMobileCode(r *http.Request, sessionID, appChallenge string) (string, error) {
	code, err := RandomString(32)
	if err != nil {
		return "", err
	}
	err = db.PutMobileAuthCode(r.Context(), d.Pool, db.MobileAuthCode{
		Code:         code,
		SessionID:    sessionID,
		AppChallenge: appChallenge,
		ExpiresAt:    time.Now().Add(db.MobileCodeTTL),
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// mobileCallbackURL builds the universal link the OAuth callback redirects to.
//
// Universal link, never a custom scheme: concertfinder:// is
// first-come-first-served on iOS, so any installed app can register it and
// receive the code. An HTTPS URL the domain claims through
// apple-app-site-association cannot be hijacked that way.
func (d *Deps) mobileCallbackURL(code string) string {
	base := d.MobileCallbackURL
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "code=" + code
}

// writeAuthError emits a JSON error. The auth handlers otherwise use
// http.Error, which sends text/plain — fine for a browser landing on a broken
// redirect, useless to URLSession, which would surface it as a decode
// failure and hide the actual reason.
func writeAuthError(w http.ResponseWriter, status int, msg string) {
	writeJSONResponse(w, status, map[string]string{"error": msg})
}

func writeJSONResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// userPayload is the shared shape of the current user, used by both
// /api/auth/me and the mobile exchange so the app decodes one model.
func userPayload(u db.User) map[string]any {
	return map[string]any{
		"id":                    u.ID,
		"spotify_user_id":       u.SpotifyUserID,
		"display_name":          u.DisplayName,
		"email":                 u.Email,
		"digest_opt_in":         u.DigestOptIn,
		"instant_notify_opt_in": u.InstantNotifyOptIn,
		"push_opt_in":           u.PushOptIn,
	}
}
