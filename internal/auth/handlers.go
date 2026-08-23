package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/spotify"
)

// Scopes requested at authorization time. See design §3.6. user-read-email
// added in Phase 3 for the daily-digest feature (design §10.3); existing
// users with pre-Phase-3 tokens must log out and re-authenticate to grant it.
var Scopes = []string{
	"user-read-recently-played",
	"user-top-read",
	"user-library-read",
	"user-follow-read",
	"playlist-read-private",
	"user-read-email",
}

const spotifyAuthorizeURL = "https://accounts.spotify.com/authorize"

type Deps struct {
	Pool          *pgxpool.Pool
	EncKey        []byte
	ClientID      string
	RedirectURI   string
	CookieDomain  string
	Handshakes    HandshakeStore
	SpotifyClient *spotify.Client
	HTTPClient    *http.Client // for Spotify token endpoint
	PostLoginURL  string       // where to send the browser after a successful callback
	// MobileCallbackURL is the universal link the callback redirects to for
	// app-initiated logins, e.g. https://<domain>/app/auth/callback. Empty
	// disables the mobile flow: /login?client=ios is refused rather than
	// silently completing into a browser session the app cannot read.
	MobileCallbackURL string
	// OnLoginSuccess is invoked after a successful callback + session
	// creation. Used by main.go to enqueue a pre-warm concert scan for the
	// new session's user so the first /me/concerts request finds a snapshot.
	// Nil is fine — no-op.
	OnLoginSuccess func(ctx context.Context, userID uuid.UUID)
}

// Mount registers /login, /callback, /logout, /me under the parent router.
// Caller is expected to mount this under /api/auth (or similar).
func Mount(r chi.Router, d *Deps) {
	r.Get("/login", d.handleLogin)
	r.Get("/callback", d.handleCallback)
	r.Post("/logout", d.handleLogout)
	// Unauthenticated: the caller is asking for the session it does not yet
	// have. Replay and forgery are handled by the one-time code and its
	// challenge, not by a session.
	r.Post("/mobile/exchange", d.handleMobileExchange)
	r.With(RequireUser(d.Pool)).Get("/me", d.handleMe)
}

func (d *Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	// App-initiated login. The challenge is the app's half of a second
	// PKCE-shaped exchange — between the app and us, distinct from the one
	// below against Spotify — and it is what makes an intercepted one-time
	// code useless without the verifier that never leaves the device.
	appChallenge := ""
	if r.URL.Query().Get("client") == ClientIOS {
		appChallenge = r.URL.Query().Get("app_challenge")
		if appChallenge == "" {
			writeAuthError(w, http.StatusBadRequest, "app_challenge is required for client=ios")
			return
		}
		if d.MobileCallbackURL == "" {
			// Without a universal link to return to there is nowhere to send
			// the code. Failing here beats completing the login into a
			// session only a browser could use.
			slog.Error("mobile login attempted but MOBILE_CALLBACK_URL is unset")
			writeAuthError(w, http.StatusNotImplemented, "mobile login is not configured on this deployment")
			return
		}
	}

	verifier, err := GenerateVerifier()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state, err := RandomString(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	handshakeKey, err := RandomString(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := d.Handshakes.Put(r.Context(), handshakeKey, verifier, state, appChallenge, HandshakeTTL); err != nil {
		slog.Error("handshake put failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setHandshakeCookie(w, d.CookieDomain, handshakeKey)

	q := url.Values{}
	q.Set("client_id", d.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", d.RedirectURI)
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", ChallengeFromVerifier(verifier))
	q.Set("state", state)
	q.Set("scope", strings.Join(Scopes, " "))

	http.Redirect(w, r, spotifyAuthorizeURL+"?"+q.Encode(), http.StatusFound)
}

func (d *Deps) handleCallback(w http.ResponseWriter, r *http.Request) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		http.Error(w, "spotify denied: "+errParam, http.StatusUnauthorized)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	hc, err := r.Cookie(handshakeCookieName)
	if err != nil {
		http.Error(w, "missing handshake cookie", http.StatusBadRequest)
		return
	}
	hs, ok := d.Handshakes.Take(r.Context(), hc.Value)
	if !ok {
		http.Error(w, "handshake expired", http.StatusBadRequest)
		return
	}
	clearHandshakeCookie(w, d.CookieDomain)
	if state != hs.State {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}

	tok, err := ExchangeCode(r.Context(), d.HTTPClient, d.ClientID, code, d.RedirectURI, hs.Verifier)
	if err != nil {
		slog.Error("token exchange failed", "err", err)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}

	me, err := d.SpotifyClient.GetMe(r.Context(), tok.AccessToken)
	if err != nil {
		// Development Mode rejection is a configuration state, not a fault of
		// the person logging in and not something a retry can clear. Say so.
		// Previously this fell through to the generic branch and reached the
		// browser as a bare 502 "spotify /me failed" — which reads as "the
		// site is broken", when the actual fix is one entry in the Spotify
		// dashboard. The operator's own login works, so nobody sees this until
		// a second person tries.
		//
		// 403 rather than 502: the request was understood and refused, and
		// nothing upstream is unhealthy.
		if errors.Is(err, spotify.ErrUserNotRegistered) {
			slog.Warn("login refused: account not on the Spotify app allowlist (Development Mode)",
				"remedy", "add the account under User Management at https://developer.spotify.com/dashboard")
			http.Error(w,
				"This app is still in Spotify's development mode, so only accounts the operator "+
					"has added can sign in. Ask them to add your Spotify account email, then try again.",
				http.StatusForbidden)
			return
		}
		slog.Error("spotify /me failed", "err", err)
		http.Error(w, "spotify /me failed", http.StatusBadGateway)
		return
	}

	ct, nonce, err := EncryptToken(d.EncKey, []byte(tok.RefreshToken))
	if err != nil {
		slog.Error("encrypt refresh token failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	user, err := db.UpsertUserBySpotifyID(r.Context(), d.Pool, db.User{
		SpotifyUserID:         me.ID,
		DisplayName:           me.DisplayName,
		EncryptedRefreshToken: ct,
		RefreshTokenNonce:     nonce,
		Email:                 me.Email,
	})
	if err != nil {
		slog.Error("upsert user failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sessionID, err := RandomString(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	expires := time.Now().Add(SessionCreatedTTL)
	if err := db.CreateSession(r.Context(), d.Pool, db.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: expires,
	}); err != nil {
		slog.Error("create session failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// An app-initiated login ends in a one-time code, not a cookie. The
	// cookie is deliberately not set in that case: ASWebAuthenticationSession
	// runs in a web context whose jar the app cannot read, so setting it
	// would leave a session usable only by that throwaway context.
	if hs.AppChallenge != "" {
		code, err := d.mintMobileCode(r, sessionID, hs.AppChallenge)
		if err != nil {
			slog.Error("mint mobile auth code failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if d.OnLoginSuccess != nil {
			d.OnLoginSuccess(r.Context(), user.ID)
		}
		http.Redirect(w, r, d.mobileCallbackURL(code), http.StatusFound)
		return
	}

	setSessionCookie(w, d.CookieDomain, sessionID, expires)

	if d.OnLoginSuccess != nil {
		d.OnLoginSuccess(r.Context(), user.ID)
	}

	target := d.PostLoginURL
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// handleLogout ends the session presented by either mechanism. Deleting the
// row is what actually logs the caller out; clearing the cookie is cosmetic
// for a bearer client, and harmless.
func (d *Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sessID, _ := sessionCredential(r); sessID != "" {
		_ = db.DeleteSession(r.Context(), d.Pool, sessID)
	}
	clearSessionCookie(w, d.CookieDomain)
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	// Middleware already fetched the full user; no need for a second query.
	full, ok := FullUserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(userPayload(full))
}
