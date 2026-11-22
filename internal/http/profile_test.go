package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/peterho/concertfinder/internal/auth"
)

func TestCleanDisplayName(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"  Ada  ", "Ada", true},
		{"Zoë Brontë", "Zoë Brontë", true},
		{"", "", false},
		{"   ", "", false},
		{"Bad\r\nBcc: x@example.com", "", false},
		{"tab\there", "", false},
		{strings.Repeat("a", maxDisplayNameRunes), strings.Repeat("a", maxDisplayNameRunes), true},
		{strings.Repeat("a", maxDisplayNameRunes+1), "", false},
		// Counted in runes, not bytes: 64 two-byte letters fit.
		{strings.Repeat("é", maxDisplayNameRunes), strings.Repeat("é", maxDisplayNameRunes), true},
		{"\xff\xfe", "", false},
	}
	for _, c := range cases {
		got, ok := cleanDisplayName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("cleanDisplayName(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// The point of the endpoint: a name set here is the one DELETE /me/account
// confirms against, so an account that arrived without one can delete itself.
func TestProfilePutStoresName(t *testing.T) {
	pool := adminTestPool(t)
	_, token := newSignedInUser(t, pool, false)

	r := chi.NewRouter()
	r.Route("/me", func(r chi.Router) {
		r.Use(auth.RequireUser(pool))
		r.Put("/profile", (&ProfileHandler{Pool: pool}).Put)
	})
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/me/profile", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	if rec := put(`{"display_name":"   "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank name: status = %d, want 400", rec.Code)
	}
	rec := put(`{"display_name":"  New Name "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"display_name":"New Name"`) {
		t.Errorf("body = %q, want the trimmed name echoed", rec.Body.String())
	}
	var stored string
	if err := pool.QueryRow(context.Background(),
		`SELECT u.display_name FROM users u JOIN sessions s ON s.user_id = u.id WHERE s.token_hash = $1`,
		auth.HashSessionToken(token)).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "New Name" {
		t.Errorf("stored display_name = %q, want %q", stored, "New Name")
	}
}
