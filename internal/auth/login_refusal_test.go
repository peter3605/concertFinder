package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/spotify"
)

// fakeSpotify answers the two Spotify requests a callback makes: the token
// exchange and GET /me. meStatus 403 is Development Mode's refusal.
type fakeSpotify struct {
	meStatus int
	meID     string
}

func (f *fakeSpotify) RoundTrip(req *http.Request) (*http.Response, error) {
	respond := func(status int, body string) *http.Response {
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}
	}
	switch {
	case req.URL.Host == "accounts.spotify.com" && req.URL.Path == "/api/token":
		return respond(http.StatusOK,
			`{"access_token":"at","token_type":"Bearer","expires_in":3600,"refresh_token":"rt"}`), nil
	case req.URL.Host == "api.spotify.com" && req.URL.Path == "/v1/me":
		if f.meStatus != 0 && f.meStatus != http.StatusOK {
			return respond(f.meStatus, `{"error":{"status":403,"message":"not registered"}}`), nil
		}
		return respond(http.StatusOK, `{"id":"`+f.meID+`","display_name":"New Person"}`), nil
	}
	return respond(http.StatusNotFound, `{}`), nil
}

type refusalFixture struct {
	pool *pgxpool.Pool
	r    chi.Router
}

func newRefusalFixture(t *testing.T, sp *fakeSpotify, inviteRequired bool, maxSpotify int) *refusalFixture {
	t.Helper()
	pool := identityTestPool(t)
	applyUpMigrations(t, pool, 1, allMigrations)
	hc := &http.Client{Transport: sp}
	d := &Deps{
		Pool:              pool,
		EncKey:            make([]byte, 32),
		ClientID:          "client",
		RedirectURI:       "https://127.0.0.1:3000/api/auth/callback",
		Handshakes:        NewDBHandshakeStore(pool),
		SpotifyClient:     spotify.NewClient(hc),
		HTTPClient:        hc,
		PostLoginURL:      "/",
		MobileCallbackURL: "https://concerts.example.com/app/auth/callback",
		InviteRequired:    inviteRequired,
		MaxSpotifyUsers:   maxSpotify,
	}
	r := chi.NewRouter()
	r.Route("/api/auth", func(r chi.Router) { Mount(r, d) })
	return &refusalFixture{pool: pool, r: r}
}

// signIn runs /login and then /callback the way a browser would, carrying
// the handshake cookie and the state Spotify echoes back. It returns the
// /login response when that refuses before Spotify is reached.
func (f *refusalFixture) signIn(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/login?"+query, nil))
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, spotifyAuthorizeURL) {
		return rec
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	var hs string
	for _, c := range rec.Result().Cookies() {
		if c.Name == handshakeCookieName {
			hs = c.Value
		}
	}
	if hs == "" {
		t.Fatal("/login set no handshake cookie")
	}
	req := httptest.NewRequest(http.MethodGet,
		"/api/auth/callback?code=authcode&state="+url.QueryEscape(u.Query().Get("state")), nil)
	req.AddCookie(&http.Cookie{Name: handshakeCookieName, Value: hs})
	rec = httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	return rec
}

func (f *refusalFixture) spotifyUsers(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := f.pool.Exec(context.Background(), `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES (gen_random_uuid(), 'existing-'||$1::int, '', 'ct'::bytea, 'n'::bytea)`, i); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
}

func (f *refusalFixture) invite(t *testing.T) string {
	t.Helper()
	code, err := db.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateInviteCode(context.Background(), f.pool, code, "test", 1, nil); err != nil {
		t.Fatal(err)
	}
	return code
}

func wantLoginRedirect(t *testing.T, rec *httptest.ResponseRecorder, code, invite string) {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 to the sign-in page; body %q", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/login" || u.Host != "" {
		t.Fatalf("Location = %q, want a relative /login", u)
	}
	if got := u.Query().Get("error"); got != code {
		t.Fatalf("error = %q, want %q", got, code)
	}
	if got := u.Query().Get("invite"); got != invite {
		t.Fatalf("invite = %q, want %q", got, invite)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.Value != "" {
			t.Fatal("a refused sign-in set a session cookie")
		}
	}
}

// Each refusal a web sign-in can meet goes back to /login with a code the
// page understands. The app-initiated version of the same refusal keeps its
// sentence, because a redirect would strand ASWebAuthenticationSession on a
// web page it never returns from.
func TestCallbackRefusalsRedirectWebAndNotApp(t *testing.T) {
	const ios = "client=ios&app_challenge=challenge"

	cases := []struct {
		name string
		// setup returns the query to start /login with (without client=ios)
		// and the invite expected back on the redirect.
		run func(t *testing.T, app bool) (*httptest.ResponseRecorder, string, string)
	}{
		{"invite required", func(t *testing.T, app bool) (*httptest.ResponseRecorder, string, string) {
			f := newRefusalFixture(t, &fakeSpotify{meID: "newcomer"}, true, 5)
			q := ""
			if app {
				q = ios
			}
			return f.signIn(t, q), LoginErrorInviteRequired, ""
		}},
		{"invite invalid at /login", func(t *testing.T, app bool) (*httptest.ResponseRecorder, string, string) {
			f := newRefusalFixture(t, &fakeSpotify{meID: "newcomer"}, true, 5)
			q := "invite=CF-NOPE-NOPE"
			if app {
				q += "&" + ios
			}
			return f.signIn(t, q), LoginErrorInviteInvalid, ""
		}},
		{"invite spent between /login and /callback", func(t *testing.T, app bool) (*httptest.ResponseRecorder, string, string) {
			f := newRefusalFixture(t, &fakeSpotify{meID: "newcomer"}, true, 5)
			code := f.invite(t)
			// /login's pre-check is read-only, so the last seat can go to
			// someone else before this callback redeems it.
			rec := httptest.NewRecorder()
			q := "invite=" + url.QueryEscape(code)
			if app {
				q += "&" + ios
			}
			f.r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/login?"+q, nil))
			if err := db.DisableInviteCode(context.Background(), f.pool, db.NormalizeInviteCode(code)); err != nil {
				t.Fatal(err)
			}
			// Replay the handshake by hand: signIn would restart /login.
			u, _ := url.Parse(rec.Header().Get("Location"))
			req := httptest.NewRequest(http.MethodGet,
				"/api/auth/callback?code=c&state="+url.QueryEscape(u.Query().Get("state")), nil)
			for _, c := range rec.Result().Cookies() {
				req.AddCookie(c)
			}
			out := httptest.NewRecorder()
			f.r.ServeHTTP(out, req)
			return out, LoginErrorInviteInvalid, ""
		}},
		{"spotify cap reached", func(t *testing.T, app bool) (*httptest.ResponseRecorder, string, string) {
			f := newRefusalFixture(t, &fakeSpotify{meID: "newcomer"}, true, 2)
			f.spotifyUsers(t, 2)
			code := f.invite(t)
			q := "invite=" + url.QueryEscape(code)
			if app {
				q += "&" + ios
			}
			return f.signIn(t, q), LoginErrorSpotifyFull, db.NormalizeInviteCode(code)
		}},
		{"development mode", func(t *testing.T, app bool) (*httptest.ResponseRecorder, string, string) {
			f := newRefusalFixture(t, &fakeSpotify{meStatus: http.StatusForbidden}, true, 5)
			code := f.invite(t)
			q := "invite=" + url.QueryEscape(code)
			if app {
				q += "&" + ios
			}
			return f.signIn(t, q), LoginErrorSpotifyNotRegistered, db.NormalizeInviteCode(code)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/web", func(t *testing.T) {
			rec, code, invite := tc.run(t, false)
			wantLoginRedirect(t, rec, code, invite)
		})
		t.Run(tc.name+"/ios", func(t *testing.T) {
			rec, _, _ := tc.run(t, true)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want the 403 sentence; Location %q", rec.Code, rec.Header().Get("Location"))
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Fatalf("app flow redirected to %q", loc)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
				t.Fatalf("Content-Type = %q, want text/plain", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// Below the cap the same Spotify signup is admitted and lands on the app,
// so the cap test above is refusing because of the cap and nothing else.
func TestCallbackAdmitsSpotifySignupBelowCap(t *testing.T) {
	f := newRefusalFixture(t, &fakeSpotify{meID: "newcomer"}, true, 2)
	f.spotifyUsers(t, 1)
	code := f.invite(t)
	rec := f.signIn(t, "invite="+url.QueryEscape(code))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status %d Location %q, want 302 to /", rec.Code, rec.Header().Get("Location"))
	}
	if sessionCookie(rec) == "" {
		t.Fatal("admitted signup set no session cookie")
	}
}

// A refusal URL never carries anything about the person, only the attempt.
func TestLoginRefusalURL(t *testing.T) {
	if got := loginRefusalURL(LoginErrorInviteRequired, ""); got != "/login?error=invite_required" {
		t.Fatalf("got %q", got)
	}
	if got := loginRefusalURL(LoginErrorSpotifyFull, "CF-AB CD"); got != "/login?error=spotify_full&invite=CF-AB+CD" {
		t.Fatalf("got %q", got)
	}
}
