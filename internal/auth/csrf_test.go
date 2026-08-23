package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// csrfHarness builds a CSRF-wrapped handler that records whether it ran.
func csrfHarness(t *testing.T) (http.Handler, *bool) {
	t.Helper()
	reached := false
	h := CSRF([]byte("test-signing-key"))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	return h, &reached
}

// A bearer-authenticated mutation carries no CSRF token and must still pass.
// This is the whole point of the exemption: a native client has no cookie jar
// and nothing to double-submit.
func TestCSRFPassesThroughForBearerAuth(t *testing.T) {
	h, reached := csrfHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/me/saved-concerts", nil)
	req = req.WithContext(withMechanism(req.Context(), MechanismBearer))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !*reached {
		t.Fatalf("bearer-authenticated POST was blocked: status %d, handler reached %v", rec.Code, *reached)
	}
}

// The other half, and the one that actually matters: the exemption must not
// have loosened the browser path. A cookie-authenticated mutation with no
// token is still a 403.
func TestCSRFStillFiresForCookieAuth(t *testing.T) {
	h, reached := csrfHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/me/saved-concerts", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "a-session"})
	req = req.WithContext(withMechanism(req.Context(), MechanismCookie))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie-authenticated POST without a token: status = %d, want 403", rec.Code)
	}
	if *reached {
		t.Fatal("handler ran despite a missing CSRF token")
	}
}

// A request that never reached RequireUser reports MechanismNone. It must be
// treated as needing the check — anything else would make the exemption
// reachable by simply not authenticating.
func TestCSRFFiresWhenMechanismUnset(t *testing.T) {
	h, reached := csrfHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/me/saved-concerts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || *reached {
		t.Fatalf("request with no recorded mechanism was not checked: status %d, reached %v", rec.Code, *reached)
	}
}

// The exemption must key off the recorded mechanism and nothing else. A
// cookie-authenticated request that merely *looks* like an app — a spoofed
// User-Agent, an Authorization header the middleware did not accept — is
// still a browser request and must still be checked. If this test ever fails,
// the CSRF defence has a bypass.
func TestCSRFIgnoresClientClaimsWithoutBearerMechanism(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*http.Request)
	}{
		{"spoofed user agent", func(r *http.Request) {
			r.Header.Set("User-Agent", "ConcertFinder/1.0 (iPhone; iOS 17.0)")
		}},
		{"client header", func(r *http.Request) {
			r.Header.Set("X-CF-Client", "ios/1.0.0 (build 42)")
		}},
		{"requested-with", func(r *http.Request) {
			r.Header.Set("X-Requested-With", "XMLHttpRequest")
		}},
		{"authorization header not honoured by the middleware", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer some-token")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, reached := csrfHarness(t)
			req := httptest.NewRequest(http.MethodPost, "/me/saved-concerts", nil)
			req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "a-session"})
			tc.prepare(req)
			// Authenticated by cookie, whatever the request claims.
			req = req.WithContext(withMechanism(req.Context(), MechanismCookie))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden || *reached {
				t.Fatalf("CSRF bypassed on a cookie-authenticated request: status %d, reached %v", rec.Code, *reached)
			}
		})
	}
}

// A cookie-authenticated mutation with the right token still works — the
// exemption must not have broken the normal browser path.
func TestCSRFAcceptsMatchingCookieToken(t *testing.T) {
	key := []byte("test-signing-key")
	const sess = "a-session"
	token := csrfToken(key, sess)

	reached := false
	h := CSRF(key)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/me/saved-concerts", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sess})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	req.Header.Set(CSRFHeaderName, token)
	req = req.WithContext(withMechanism(req.Context(), MechanismCookie))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !reached {
		t.Fatalf("valid cookie CSRF was rejected: status %d, reached %v", rec.Code, reached)
	}
}

// GET is exempt regardless of mechanism, as it was before.
func TestCSRFAllowsSafeMethods(t *testing.T) {
	h, reached := csrfHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/me/concerts", nil)
	req = req.WithContext(withMechanism(req.Context(), MechanismCookie))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !*reached {
		t.Fatalf("GET was blocked: status %d, reached %v", rec.Code, *reached)
	}
}
