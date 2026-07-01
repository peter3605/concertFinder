package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/spotify"
)

// Scopes requested at authorization time. See design §3.6.
var Scopes = []string{
	"user-read-recently-played",
	"user-top-read",
	"user-library-read",
	"user-follow-read",
	"playlist-read-private",
}

const spotifyAuthorizeURL = "https://accounts.spotify.com/authorize"

type Deps struct {
	Pool          *pgxpool.Pool
	EncKey        []byte
	ClientID      string
	RedirectURI   string
	CookieDomain  string
	Handshakes    *HandshakeStore
	SpotifyClient *spotify.Client
	HTTPClient    *http.Client // for Spotify token endpoint
	PostLoginURL  string       // where to send the browser after a successful callback
}

// Mount registers /login, /callback, /logout, /me under the parent router.
// Caller is expected to mount this under /api/auth (or similar).
func Mount(r chi.Router, d *Deps) {
	r.Get("/login", d.handleLogin)
	r.Get("/callback", d.handleCallback)
	r.Post("/logout", d.handleLogout)
	r.With(RequireUser(d.Pool)).Get("/me", d.handleMe)
}

func (d *Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
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
	d.Handshakes.Put(handshakeKey, verifier, state, HandshakeTTL)
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
	hs, ok := d.Handshakes.Take(hc.Value)
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
	setSessionCookie(w, d.CookieDomain, sessionID, expires)

	target := d.PostLoginURL
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (d *Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		_ = db.DeleteSession(r.Context(), d.Pool, c.Value)
	}
	clearSessionCookie(w, d.CookieDomain)
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":              u.ID,
		"spotify_user_id": u.SpotifyUserID,
		"display_name":    u.DisplayName,
	})
}
