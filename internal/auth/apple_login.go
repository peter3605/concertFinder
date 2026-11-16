package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/apple"
	"github.com/peterho/concertfinder/internal/db"
)

// Sign in with Apple (CF-35, docs/public-path.md section 4.1).
//
//	Web:  GET  /api/auth/apple/login     -> appleid.apple.com/auth/authorize
//	      POST /api/auth/apple/callback  <- Apple's form_post; sets cf_session
//	iOS:  POST /api/auth/apple/native    identity token from the native sheet;
//	                                     answers with a bearer session
//
// Both routes verify the identity token the client presents (signature, iss,
// aud, exp, nonce), each accepting only its own audience, then exchange the
// authorization code for the refresh token that account deletion revokes.
const (
	AppleCallbackPath = "/api/auth/apple/callback"
	appleAuthorizeURL = "https://appleid.apple.com/auth/authorize"

	// appleHandshakeCookie is separate from cf_handshake because its
	// attributes have to differ. Requesting name and email makes Apple
	// return by a cross-site form POST, and browsers withhold SameSite=Lax
	// cookies on a cross-site POST -- so with the shared Lax cookie every
	// web sign-in would end in "missing handshake cookie". SameSite=None is
	// confined to this cookie and to the Apple path; the session cookie
	// stays Lax.
	appleHandshakeCookie = "cf_apple_handshake"
	appleCookiePath      = "/api/auth/apple"

	// LoginErrorAppleFailed sends a failed Apple sign-in back to /login.
	LoginErrorAppleFailed = "apple_failed"
)

// AppleVerifier is the part of *apple.Client the handlers use; tests use the
// real client against appletest.Server.
type AppleVerifier interface {
	VerifyIdentityToken(ctx context.Context, token, audience, rawNonce string) (apple.Claims, error)
	RedeemCode(ctx context.Context, clientID, code, redirectURI string) (apple.TokenResponse, error)
}

// AppleLogin serves the Apple routes. Mount it only when Sign in with Apple
// is configured; unmounted, the routes 404.
type AppleLogin struct {
	Pool   *pgxpool.Pool
	Apple  AppleVerifier
	EncKey []byte
	// ServicesID is the web client_id and the only aud the callback accepts.
	ServicesID string
	// BundleID is the iOS aud. Empty leaves /apple/native answering 501.
	BundleID       string
	SiteBaseURL    string
	CookieDomain   string
	PostLoginURL   string
	InviteRequired bool
	Handshakes     HandshakeStore
	OnLoginSuccess func(ctx context.Context, userID uuid.UUID)
}

// Mount registers the routes under the /api/auth router.
func (a *AppleLogin) Mount(r chi.Router) {
	r.Get("/apple/login", a.handleLogin)
	r.Post("/apple/callback", a.handleCallback)
	r.Post("/apple/native", a.handleNative)
}

func (a *AppleLogin) redirectURI() string {
	return strings.TrimRight(a.SiteBaseURL, "/") + AppleCallbackPath
}

func (a *AppleLogin) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Read-only, like /api/auth/login: a typo is caught before Apple's
	// sheet, and nothing is spent until the callback creates an account.
	// A blank code is not refused here, for the same reason -- only the
	// callback knows whether this is a signup.
	invite := db.NormalizeInviteCode(r.URL.Query().Get("invite"))
	if a.InviteRequired && invite != "" {
		ok, err := db.InviteCodeUsable(r.Context(), a.Pool, invite)
		if err != nil {
			slog.Error("apple: invite pre-check failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Redirect(w, r, loginRefusalURL(LoginErrorInviteInvalid, ""), http.StatusFound)
			return
		}
	}
	state, err := RandomString(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	nonce, err := RandomString(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	key, err := RandomString(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := a.Handshakes.Put(r.Context(), PendingHandshake{
		Key:        key,
		State:      state,
		InviteCode: invite,
		Nonce:      nonce,
	}, HandshakeTTL); err != nil {
		slog.Error("apple: handshake put failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, appleHandshakeCookieFor(a.CookieDomain, key, int(HandshakeTTL.Seconds())))

	q := url.Values{}
	q.Set("client_id", a.ServicesID)
	q.Set("redirect_uri", a.redirectURI())
	// id_token alongside code: the token posted back is the one carrying
	// the nonce, and it is what gets verified.
	q.Set("response_type", "code id_token")
	// form_post is required by Apple whenever a scope is requested.
	q.Set("response_mode", "form_post")
	q.Set("scope", "name email")
	q.Set("state", state)
	q.Set("nonce", apple.HashNonce(nonce))
	http.Redirect(w, r, appleAuthorizeURL+"?"+q.Encode(), http.StatusFound)
}

func appleHandshakeCookieFor(domain, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     appleHandshakeCookie,
		Value:    value,
		Domain:   domain,
		Path:     appleCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	}
}

func (a *AppleLogin) handleCallback(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	// The handshake is spent first, whatever happens next, so one Apple
	// response can never be replayed against it.
	var hs *handshake
	if c, err := r.Cookie(appleHandshakeCookie); err == nil {
		hs, _ = a.Handshakes.Take(r.Context(), c.Value)
	}
	if hs != nil {
		http.SetCookie(w, appleHandshakeCookieFor(a.CookieDomain, "", -1))
	}

	if e := r.PostFormValue("error"); e != "" {
		// user_cancelled_authorize is the person closing Apple's sheet,
		// which is a choice rather than a failure.
		if e == "user_cancelled_authorize" {
			http.Redirect(w, r, loginPagePath, http.StatusSeeOther)
			return
		}
		slog.Warn("apple: authorization returned an error", "error", e)
		http.Redirect(w, r, loginRefusalURL(LoginErrorAppleFailed, ""), http.StatusSeeOther)
		return
	}
	if hs == nil || hs.Nonce == "" {
		http.Error(w, "missing or expired handshake; start the sign-in again", http.StatusBadRequest)
		return
	}
	if r.PostFormValue("state") != hs.State {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	code := r.PostFormValue("code")
	idToken := r.PostFormValue("id_token")
	if code == "" || idToken == "" {
		http.Error(w, "missing code or identity token", http.StatusBadRequest)
		return
	}
	// Apple posts "user" on the first authorization only. It is read before
	// anything can fail after it, because there is no second chance.
	u := apple.ParseUserField(r.PostFormValue("user"))

	userID, refusal, err := a.signIn(r.Context(), signInInput{
		idToken:     idToken,
		code:        code,
		audience:    a.ServicesID,
		redirectURI: a.redirectURI(),
		rawNonce:    hs.Nonce,
		displayName: apple.DisplayName(u.Name.FirstName, u.Name.LastName),
		invite:      hs.InviteCode,
	})
	if refusal != "" {
		invite := ""
		if refusal != LoginErrorInviteInvalid && refusal != LoginErrorInviteRequired {
			invite = hs.InviteCode
		}
		http.Redirect(w, r, loginRefusalURL(refusal, invite), http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Redirect(w, r, loginRefusalURL(LoginErrorAppleFailed, hs.InviteCode), http.StatusSeeOther)
		return
	}

	token, expires, err := issueSession(r.Context(), a.Pool, userID)
	if err != nil {
		slog.Error("apple: create session failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The session cookie stays SameSite=Lax. Setting it on the response to
	// Apple's cross-site POST is allowed -- SameSite governs sending, not
	// storing -- and the redirect below is a top-level GET, which carries
	// Lax cookies.
	setSessionCookie(w, a.CookieDomain, token, expires)
	if a.OnLoginSuccess != nil {
		a.OnLoginSuccess(r.Context(), userID)
	}
	target := a.PostLoginURL
	if target == "" {
		target = "/"
	}
	// 303 so the browser follows with a GET, not a re-POST of Apple's form.
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// appleNativeRequest is the body of POST /api/auth/apple/native, built from
// ASAuthorizationAppleIDCredential. Nonce is the raw value whose SHA-256 the
// app set as the request's nonce. GivenName and FamilyName are present on
// the first authorization only.
type appleNativeRequest struct {
	IdentityToken     string `json:"identity_token"`
	AuthorizationCode string `json:"authorization_code"`
	Nonce             string `json:"nonce"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	Invite            string `json:"invite"`
}

// handleNative signs the app in. It answers with the session directly, as
// email's /code does, rather than through the mobile one-time-code exchange:
// that exchange exists to keep a session out of a redirect URL, and here
// nothing travels in a URL -- the request and the response are one POST.
//
// JSON only, which also keeps it from being a login-CSRF target: a page
// elsewhere cannot send application/json without a preflight this server
// never grants.
func (a *AppleLogin) handleNative(w http.ResponseWriter, r *http.Request) {
	if a.BundleID == "" {
		writeAuthError(w, http.StatusNotImplemented, "Sign in with Apple for the app is not configured on this deployment")
		return
	}
	if !isJSON(r) {
		writeAuthError(w, http.StatusUnsupportedMediaType, "send application/json")
		return
	}
	var req appleNativeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil ||
		req.IdentityToken == "" || req.AuthorizationCode == "" || req.Nonce == "" {
		writeAuthError(w, http.StatusBadRequest, "identity_token, authorization_code and nonce are required")
		return
	}
	userID, refusal, err := a.signIn(r.Context(), signInInput{
		idToken:     req.IdentityToken,
		code:        req.AuthorizationCode,
		audience:    a.BundleID,
		rawNonce:    req.Nonce,
		displayName: apple.DisplayName(req.GivenName, req.FamilyName),
		invite:      req.Invite,
	})
	switch {
	case refusal != "":
		writeJSONResponse(w, http.StatusForbidden, map[string]string{
			"error": appleRefusalMessage(refusal),
			"code":  refusal,
		})
		return
	case errors.Is(err, errAppleToken):
		writeAuthError(w, http.StatusUnauthorized, "the Apple identity token could not be verified")
		return
	case err != nil:
		writeAuthError(w, http.StatusBadGateway, "Sign in with Apple failed; try again")
		return
	}
	if writeBearerSession(w, r, a.Pool, userID, "apple native sign-in") && a.OnLoginSuccess != nil {
		a.OnLoginSuccess(r.Context(), userID)
	}
}

func appleRefusalMessage(code string) string {
	switch code {
	case LoginErrorInviteRequired:
		return "ConcertFinder is invite-only at the moment, so creating a new account needs an invite code."
	case LoginErrorInviteInvalid:
		return "That invite code is not valid. It may have already been used, or it may have expired."
	}
	return "Sign in with Apple was refused."
}

var errAppleToken = errors.New("apple identity token rejected")

type signInInput struct {
	idToken, code, audience, redirectURI, rawNonce string
	displayName, invite                            string
}

// signIn is the part both routes share: verify, exchange, admit. A non-empty
// refusal is a LoginError code for a sign-in that was understood and turned
// down; err is everything else.
func (a *AppleLogin) signIn(ctx context.Context, in signInInput) (uuid.UUID, string, error) {
	claims, err := a.Apple.VerifyIdentityToken(ctx, in.idToken, in.audience, in.rawNonce)
	if err != nil {
		slog.Warn("apple: identity token rejected", "err", err, "audience", in.audience)
		return uuid.Nil, "", errAppleToken
	}
	tok, err := a.Apple.RedeemCode(ctx, in.audience, in.code, in.redirectURI)
	if err != nil {
		slog.Error("apple: code exchange failed", "err", err, "audience", in.audience)
		return uuid.Nil, "", err
	}
	// The code must belong to the user the verified token names, or a code
	// from somebody else's authorization would attach their refresh token
	// (and therefore their revocation) to this account.
	if apple.SubjectOf(tok.IDToken) != claims.Subject {
		slog.Warn("apple: exchanged code belongs to a different subject than the identity token")
		return uuid.Nil, "", errAppleToken
	}
	ct, nonce, err := EncryptToken(a.EncKey, []byte(tok.RefreshToken))
	if err != nil {
		slog.Error("apple: encrypt refresh token failed", "err", err)
		return uuid.Nil, "", err
	}
	// Only the signed token's email. The web callback's "user" field also
	// carries one, but the browser posts that form and can put anything in
	// it, which would let an account point our mail at any address.
	email := claims.Email
	userID, created, err := db.AdmitApple(ctx, a.Pool, db.AppleSignIn{
		Subject:               claims.Subject,
		Email:                 email,
		DisplayName:           in.displayName,
		ClientID:              in.audience,
		EncryptedRefreshToken: ct,
		RefreshTokenNonce:     nonce,
	}, in.invite, a.InviteRequired)
	switch {
	case errors.Is(err, db.ErrInviteRequired):
		// Names nobody: no account was created. Apple has already spent the
		// one response that carries the name, so if this person comes back
		// with a code their account starts without one (onboarding asks;
		// removing the app under Apple ID settings resets it).
		slog.Info("apple signup refused: no invite code", "name_supplied", in.displayName != "")
		return uuid.Nil, LoginErrorInviteRequired, nil
	case errors.Is(err, db.ErrInviteInvalid):
		slog.Info("apple signup refused: invite not redeemable", "code", in.invite)
		return uuid.Nil, LoginErrorInviteInvalid, nil
	case err != nil:
		slog.Error("apple: admission failed", "err", err)
		return uuid.Nil, "", err
	}
	if created {
		slog.Info("apple signup", "user", userID, "name_supplied", in.displayName != "", "email_supplied", email != "")
	}
	return userID, "", nil
}
