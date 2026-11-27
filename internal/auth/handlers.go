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
	// InviteRequired gates signups on an invite code. See
	// config.Config.InviteRequired for why it defaults on. Logins by
	// existing users ignore it entirely.
	InviteRequired bool
	// MaxSpotifyUsers caps how many Spotify accounts may exist
	// (SPOTIFY_MAX_USERS). Like InviteRequired it only ever refuses a
	// signup. Callers must set it: zero is a real cap ("no new Spotify
	// accounts"), and db.NoSpotifyCap disables it.
	MaxSpotifyUsers int
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
	// Unauthenticated and read-only: the sign-up screens ask what kind of
	// code they hold before offering Continue with Spotify (CF-B33).
	r.Get("/invite", d.handleInviteCheck)
	r.With(RequireUser(d.Pool)).Get("/me", d.handleMe)
}

// handleInviteCheck answers GET /api/auth/invite?code=... with
// {"usable": bool, "spotify": bool}: whether the code would be accepted now
// and whether it admits a Spotify signup. The sign-up screens use it to offer
// Continue with Spotify only to someone holding a Spotify invite.
//
// It reveals nothing /login does not already: /login with ?invite= answers a
// refusal or a redirect to Spotify according to the same two facts. It sits
// behind the same per-IP /api/auth bucket, and against ~39 bits of code that
// bucket is what makes guessing pointless. It never reserves or spends
// anything, and an unknown or blank code is simply usable=false rather than
// an error, since a half-typed code is the ordinary case for a screen that
// asks as the person types.
//
// With INVITE_REQUIRED off there is no gate to describe, so every code is
// reported usable and Spotify-capable, matching what /login and the callback
// would then do with it.
func (d *Deps) handleInviteCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	out := db.InviteCheck{Usable: true, Spotify: true}
	if d.InviteRequired {
		out = db.InviteCheck{}
		if code := db.NormalizeInviteCode(r.URL.Query().Get("code")); code != "" {
			check, err := db.CheckInviteCode(r.Context(), d.Pool, code)
			if err != nil {
				slog.Error("invite check failed", "err", err)
				writeAuthError(w, http.StatusInternalServerError, "internal error")
				return
			}
			out = check
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"usable": out.Usable, "spotify": out.Spotify})
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

	// The code is checked here so somebody with a typo finds out before
	// being sent through Spotify's consent screen, and it is checked
	// read-only: redemption happens at /callback, and only if this login
	// turns out to be a signup. A login started and abandoned must not spend
	// anyone's invite.
	//
	// A blank code is NOT refused here. This handler cannot tell a signup
	// from a returning user -- that needs the Spotify identity, which only
	// exists after the round trip -- so refusing one now would lock out every
	// existing user the moment the gate came on. The callback is the only
	// place with enough information to require a code, so it is the only
	// place that does.
	invite := db.NormalizeInviteCode(r.URL.Query().Get("invite"))
	if d.InviteRequired && invite != "" {
		check, err := db.CheckInviteCode(r.Context(), d.Pool, invite)
		if err != nil {
			slog.Error("invite pre-check failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !check.Usable {
			// A redirect rather than JSON for both clients: /login is reached
			// through a browser or ASWebAuthenticationSession, not URLSession.
			d.refuse(w, r, appChallenge != "", LoginErrorInviteInvalid, "")
			return
		}
		if !check.Spotify {
			// A good code of the wrong kind (CF-B33): it admits email and
			// Apple signups, and nothing says its holder is on Spotify's
			// allowlist, so sending them through consent would end on
			// spotify_not_registered. Refused here, before the round trip,
			// and echoed back unspent so the sign-up page can carry it to
			// email. db.UpsertUserWithAdmission repeats the check at
			// redemption, which is the authority; this is the courtesy.
			d.refuse(w, r, appChallenge != "", LoginErrorInviteNotSpotify, invite)
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
	if err := d.Handshakes.Put(r.Context(), PendingHandshake{
		Key:          handshakeKey,
		Verifier:     verifier,
		State:        state,
		AppChallenge: appChallenge,
		InviteCode:   invite,
	}, HandshakeTTL); err != nil {
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
			// Nothing was admitted, so an invite this person brought is
			// unspent and goes back to the page with them.
			d.refuse(w, r, hs.AppChallenge != "", LoginErrorSpotifyNotRegistered, hs.InviteCode)
			return
		}
		slog.Error("spotify /me failed", "err", err)
		http.Error(w, "spotify /me failed", http.StatusBadGateway)
		return
	}
	// Answers docs/public-path.md section 4.4 -- does Spotify still return
	// the address at all? -- from the next real sign-in. A boolean, never
	// the value.
	slog.Info("spotify login profile", "spotify_me_email_present", me.Email != "")

	ct, nonce, err := EncryptToken(d.EncKey, []byte(tok.RefreshToken))
	if err != nil {
		slog.Error("encrypt refresh token failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The admission gate. This call updates the row when Spotify's account is
	// already known and creates one when it is not, and only the second case
	// consults the invite code -- so a returning user never needs one and
	// every account predating migration 0021 is grandfathered for free.
	user, err := db.UpsertUserWithAdmission(r.Context(), d.Pool, db.User{
		SpotifyUserID:         me.ID,
		DisplayName:           me.DisplayName,
		EncryptedRefreshToken: ct,
		RefreshTokenNonce:     nonce,
		Email:                 me.Email,
	}, hs.InviteCode, d.InviteRequired, d.MaxSpotifyUsers)
	switch {
	case errors.Is(err, db.ErrSpotifyFull):
		// Checked before the invite, so a code this person holds is still
		// unspent. Like the other refusals it names nobody: no user row was
		// written, so there is nothing of theirs to log.
		slog.Info("signup refused: spotify user cap reached")
		d.refuse(w, r, hs.AppChallenge != "", LoginErrorSpotifyFull, hs.InviteCode)
		return
	case errors.Is(err, db.ErrInviteRequired):
		// A refusal, not a fault, and the same shape as the Development Mode
		// branch above: the person did nothing wrong and a retry will not
		// help, so say what would.
		// Deliberately identifies nobody. A refused signup writes no user
		// row, so logging the Spotify ID would retain an identifier for
		// somebody who does not have an account -- the one case where we
		// hold Spotify-derived data for a non-user. The event is the useful
		// part: it tells the operator people are arriving without codes.
		slog.Info("signup refused: no invite code")
		d.refuse(w, r, hs.AppChallenge != "", LoginErrorInviteRequired, "")
		return
	case errors.Is(err, db.ErrInviteInvalid):
		// Distinct from the above on purpose. "Your code did not work" and
		// "you need a code" send the reader to different places.
		//
		// Reachable even after /login accepted the code: the pre-check is
		// read-only, so the last seat can be taken between the two by someone
		// else. That race resolves here because this is where the atomic
		// redemption is.
		// The code, not the person: it is what the operator needs to answer
		// "why didn't my invite work", and unlike the Spotify ID it names
		// something they issued rather than somebody who was turned away.
		slog.Info("signup refused: invite not redeemable", "code", hs.InviteCode)
		d.refuse(w, r, hs.AppChallenge != "", LoginErrorInviteInvalid, "")
		return
	case errors.Is(err, db.ErrInviteNotSpotify):
		// /login refuses this before the round trip, so arriving here means
		// the callback was reached some other way -- a hand-built handshake,
		// or a code whose kind changed in between. Decided before the
		// redemption, so the code is unspent and goes back with the refusal.
		slog.Info("signup refused: invite is not a Spotify invite", "code", hs.InviteCode)
		d.refuse(w, r, hs.AppChallenge != "", LoginErrorInviteNotSpotify, hs.InviteCode)
		return
	case err != nil:
		slog.Error("upsert user failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The row id and the credential are two different values since migration
	// 0018. The id is opaque and is what other tables reference; the token is
	// what the caller presents, and only its hash is ever written down.
	sessionRowID := uuid.NewString()
	sessionToken, err := RandomString(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	expires := time.Now().Add(SessionCreatedTTL)
	sess := db.Session{
		ID:        sessionRowID,
		TokenHash: HashSessionToken(sessionToken),
		UserID:    user.ID,
		ExpiresAt: expires,
	}
	// An app-initiated login ends in a one-time code, not a cookie. The
	// cookie is deliberately not set in that case: ASWebAuthenticationSession
	// runs in a web context whose jar the app cannot read, so setting it
	// would leave a session usable only by that throwaway context.
	//
	// The token minted just above is discarded for that path and the row is
	// written with no hash at all, so the session exists but authenticates
	// nobody until POST /api/auth/mobile/exchange claims it. That is what
	// lets the app's credential be created at redemption time and travel
	// only in the exchange response — the alternative is escrowing a working
	// token in mobile_auth_codes, which is the exact storage this change
	// exists to remove.
	if hs.AppChallenge != "" {
		sess.TokenHash = ""
	}
	if err := db.CreateSession(r.Context(), d.Pool, sess); err != nil {
		slog.Error("create session failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if hs.AppChallenge != "" {
		code, err := d.mintMobileCode(r, sessionRowID, hs.AppChallenge)
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

	setSessionCookie(w, d.CookieDomain, sessionToken, expires)

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
	if token, _ := sessionCredential(r); token != "" {
		_ = db.DeleteSessionByTokenHash(r.Context(), d.Pool, HashSessionToken(token))
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

// Refusal codes both clients understand, carried as /login?error=<code> on
// the web and as MOBILE_CALLBACK_URL?error=<code> to the app.
// web/src/pages/login.tsx and the iOS AuthController.refusal each map them
// to a message, so renaming one here strands it in both as a generic error
// (and the iOS copy is on phones that never update). Like the refusals themselves they describe the attempt, never the
// person: no Spotify ID or name ever rides in the URL.
const (
	LoginErrorInviteRequired       = "invite_required"
	LoginErrorInviteInvalid        = "invite_invalid"
	LoginErrorInviteNotSpotify     = "invite_not_spotify"
	LoginErrorSpotifyFull          = "spotify_full"
	LoginErrorSpotifyNotRegistered = "spotify_not_registered"
)

// loginPagePath is the SPA route of the sign-in page. Relative, like
// PostLoginURL, so the redirect stays on whichever origin served the callback.
const loginPagePath = "/login"

// refuse ends a sign-in that was understood and turned down.
//
// The web flow goes back to the sign-in page, which shows the reason beside
// the control that caused it. Before CF-41 it ended on a text/plain 403 with
// no way back, reached after Spotify's consent screen, which is the moment a
// new person is least likely to try again.
//
// An app-initiated login goes back to the app the same way: to
// MOBILE_CALLBACK_URL with error=<code> in place of code=<one-time code>.
// It runs inside ASWebAuthenticationSession, which ends only on that
// universal link. Until CF-44 it ended on a text/plain 403 instead, a bare
// page whose only exit was Cancel — which the app reads as the user
// dismissing the sheet, so the reason was shown nowhere. Each client owns
// the wording for a code (login.tsx's REFUSALS, AuthController.refusal);
// the server sends only the code.
//
// invite is passed back only when the refusal spent nothing, so a person
// holding a valid code does not have to find and retype it. Nothing else
// rides in either URL: the refusal describes the attempt, never the person.
func (d *Deps) refuse(w http.ResponseWriter, r *http.Request, appFlow bool, code, invite string) {
	if appFlow {
		http.Redirect(w, r, d.mobileRefusalURL(code, invite), http.StatusFound)
		return
	}
	http.Redirect(w, r, loginRefusalURL(code, invite), http.StatusFound)
}

func loginRefusalURL(code, invite string) string {
	return loginPagePath + "?" + refusalQuery(code, invite)
}

// mobileRefusalURL is mobileCallbackURL's refusal twin. It carries no code,
// so the app has nothing to send to /mobile/exchange.
func (d *Deps) mobileRefusalURL(code, invite string) string {
	sep := "?"
	if strings.Contains(d.MobileCallbackURL, "?") {
		sep = "&"
	}
	return d.MobileCallbackURL + sep + refusalQuery(code, invite)
}

func refusalQuery(code, invite string) string {
	q := url.Values{}
	q.Set("error", code)
	if invite != "" {
		q.Set("invite", invite)
	}
	return q.Encode()
}
