package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/peterho/concertfinder/internal/auth"
)

// Opting into email with no address on file used to answer 400 "log in again
// to grant email access", which no re-login could satisfy once Spotify stopped
// returning the address (docs/public-path.md section 4.4). CF-33 replaced it
// with the confirm-an-address step, and the refusal now names that remedy.
func TestEmailOptInWithoutAddressPointsAtVerification(t *testing.T) {
	pool := adminTestPool(t)
	_, token := newSignedInUser(t, pool, false)

	r := chi.NewRouter()
	r.Route("/me", func(r chi.Router) {
		r.Use(auth.RequireUser(pool))
		r.Put("/email-prefs", (&EmailPrefsHandler{Pool: pool}).Put)
	})
	req := httptest.NewRequest(http.MethodPut, "/me/email-prefs", strings.NewReader(`{"digest_opt_in":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "email_required") {
		t.Errorf("body = %q, want the email_required code", body)
	}
	if strings.Contains(body, "log in again") {
		t.Errorf("body = %q still tells the user to log in again", body)
	}
}
