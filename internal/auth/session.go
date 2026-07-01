package auth

import (
	"net/http"
	"time"
)

const (
	SessionCookieName   = "cf_session"
	handshakeCookieName = "cf_handshake"

	SessionCreatedTTL = 90 * 24 * time.Hour // hard cap
	SessionIdleTTL    = 30 * 24 * time.Hour // last_seen refresh window
	HandshakeTTL      = 10 * time.Minute
)

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
