package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"
)

const (
	SessionCookieName   = "cf_session"
	handshakeCookieName = "cf_handshake"

	SessionCreatedTTL = 90 * 24 * time.Hour // hard cap
	HandshakeTTL      = 10 * time.Minute
)

// HashSessionToken maps a session token to what the database stores for it.
//
// Plain, unsalted SHA-256 is correct here rather than a password hash: the
// input is 32 bytes straight from crypto/rand, so there is no guessable
// keyspace for a work factor to slow down and no two users can collide into a
// shared rainbow-table entry. What it buys is that sessions.token_hash is not
// a credential — the nightly pg_dump sitting in S3 used to be a file of live
// logins for every signed-in user, since the stored value *was* the cookie and
// *was* the iOS bearer token.
//
// Every read, touch and delete goes through this. Nothing else may hash a
// session token, or two spellings of "the same" hash would authenticate
// differently.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func setSessionCookie(w http.ResponseWriter, domain, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Domain:   domain,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, domain string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Domain:   domain,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func setHandshakeCookie(w http.ResponseWriter, domain, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     handshakeCookieName,
		Value:    value,
		Domain:   domain,
		Path:     "/",
		MaxAge:   int(HandshakeTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearHandshakeCookie(w http.ResponseWriter, domain string) {
	http.SetCookie(w, &http.Cookie{
		Name:     handshakeCookieName,
		Value:    "",
		Domain:   domain,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
