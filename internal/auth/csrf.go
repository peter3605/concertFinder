package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"
)

// CSRFCookieName is the cookie the frontend echoes back via the CSRF header.
const CSRFCookieName = "cf_csrf"

// CSRFHeaderName is what mutating requests must send matching the cookie.
const CSRFHeaderName = "X-CSRF-Token"

// CSRFTokenTTL is refreshed opportunistically on any authenticated request.
const CSRFTokenTTL = 24 * time.Hour

// CSRF returns a middleware that:
//  1. On any request without a cf_csrf cookie, sets one to an HMAC over the
//     session cookie value (so it's opaque, stable-per-session, unforgeable
//     by anyone without the signing key).
//  2. On POST/PUT/DELETE requests, requires X-CSRF-Token to match the cookie.
//  3. GET/HEAD/OPTIONS pass through (per RFC 7231, they're supposed to be safe).
//  4. Bearer-authenticated requests pass through entirely.
//
// Cookie is SameSite=Strict + Secure + Not-HttpOnly (frontend JS reads it to
// populate the header). SameSite=Strict alone would suffice for most
// cross-site attack surfaces; the explicit header check is defense-in-depth.
//
// On (4): CSRF exists because a cookie is ambient authority — the browser
// attaches it to a cross-site request the user never intended, and the token
// proves the caller could read a same-origin response. A bearer token is not
// ambient. Nothing attaches it automatically, so there is no forgery to
// prevent and a native client has no reason to carry a token at all.
//
// The signal is RequireUser's recorded Mechanism and nothing else. It must
// not be a User-Agent test, an X-Requested-With test, or a "looks like an
// app" test: every one of those is a string an attacker's page can put on a
// cookie-bearing request, which would make this a CSRF bypass rather than an
// exemption. MechanismBearer is only ever set when the credential arrived in
// a header the browser will not send on its own, and a request that never
// reached RequireUser reports MechanismNone and is still checked. That
// ordering is load-bearing: CSRF must be mounted *after* RequireUser.
func CSRF(signingKey []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if MechanismFromContext(r.Context()) == MechanismBearer {
				next.ServeHTTP(w, r)
				return
			}
			sessCookie, _ := r.Cookie(SessionCookieName)
			sessID := ""
			if sessCookie != nil {
				sessID = sessCookie.Value
			}
			token := csrfToken(signingKey, sessID)

			// Ensure the cookie is set for this session.
			existing, _ := r.Cookie(CSRFCookieName)
			if existing == nil || existing.Value != token {
				http.SetCookie(w, &http.Cookie{
					Name:     CSRFCookieName,
					Value:    token,
					Path:     "/",
					Expires:  time.Now().Add(CSRFTokenTTL),
					HttpOnly: false, // JS reads this to populate the header
					Secure:   true,
					SameSite: http.SameSiteStrictMode,
				})
			}

			// Only guard mutating methods.
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
				got := r.Header.Get(CSRFHeaderName)
				if got == "" || !hmac.Equal([]byte(got), []byte(token)) {
					http.Error(w, "csrf token missing or invalid", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// csrfToken derives an opaque, deterministic token from (signingKey, sessionID).
// Same session → same token, so double-submit works. Different sessions → different
// tokens. Attackers with neither the signing key nor the session cookie can't guess it.
func csrfToken(signingKey []byte, sessID string) string {
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte("csrf|"))
	mac.Write([]byte(sessID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
