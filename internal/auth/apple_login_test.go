package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/apple"
	"github.com/peterho/concertfinder/internal/apple/appletest"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/testdb"
)

const (
	appleServices = "com.example.app.web"
	appleBundle   = "com.example.app"
	appleSite     = "https://concertfinder.test"
)

type appleFixture struct {
	pool   *pgxpool.Pool
	srv    *appletest.Server
	h      *AppleLogin
	router chi.Router
	logins []uuid.UUID
	// sub is the subject the fake Apple's exchanged id_token names.
	sub string
}

func newAppleFixture(t *testing.T, inviteRequired bool) *appleFixture {
	t.Helper()
	pool := testdb.Isolated(t, "../../migrations")
	srv := appletest.NewServer(t)
	client, err := apple.New(apple.Config{
		TeamID:  "TEAM123",
		KeyID:   "KEY123",
		P8Key:   srv.EscapedP8,
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &appleFixture{pool: pool, srv: srv, sub: "001.apple-subject"}
	srv.IDTokenForCode = func(form url.Values) string {
		return srv.Sign(map[string]any{
			"iss": apple.Issuer, "aud": form.Get("client_id"), "sub": f.sub,
			"exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix(),
		})
	}
	f.h = &AppleLogin{
		Pool:           pool,
		Apple:          client,
		EncKey:         make([]byte, 32),
		ServicesID:     appleServices,
		BundleID:       appleBundle,
		SiteBaseURL:    appleSite,
		PostLoginURL:   "/",
		InviteRequired: inviteRequired,
		Handshakes:     NewDBHandshakeStore(pool),
		OnLoginSuccess: func(_ context.Context, id uuid.UUID) { f.logins = append(f.logins, id) },
	}
	r := chi.NewRouter()
	r.Route("/api/auth", func(r chi.Router) { f.h.Mount(r) })
	f.router = r
	return f
}

func (f *appleFixture) idToken(aud, rawNonce string, extra map[string]any) string {
	c := map[string]any{
		"iss": apple.Issuer, "aud": aud, "sub": f.sub,
		"exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix(),
		"nonce": apple.HashNonce(rawNonce), "email": "Person@privaterelay.appleid.com",
	}
	for k, v := range extra {
		c[k] = v
	}
	return f.srv.Sign(c)
}

// startWeb runs GET /apple/login and returns the handshake cookie and the
// raw nonce stored behind it.
func (f *appleFixture) startWeb(t *testing.T, invite string) (*http.Cookie, string, *httptest.ResponseRecorder) {
	t.Helper()
	target := "/api/auth/apple/login"
	if invite != "" {
		target += "?invite=" + url.QueryEscape(invite)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var hc *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == appleHandshakeCookie {
			hc = c
		}
	}
	if hc == nil {
		return nil, "", rec
	}
	var nonce, state string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT nonce, state FROM oauth_handshakes WHERE handshake_key = $1`, hc.Value).Scan(&nonce, &state); err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	return hc, nonce, rec
}

func (f *appleFixture) stateFor(t *testing.T, hc *http.Cookie) string {
	var state string
	_ = f.pool.QueryRow(context.Background(),
		`SELECT state FROM oauth_handshakes WHERE handshake_key = $1`, hc.Value).Scan(&state)
	return state
}

func (f *appleFixture) callback(hc *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, AppleCallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hc != nil {
		req.AddCookie(&http.Cookie{Name: hc.Name, Value: hc.Value})
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func newInvite(t *testing.T, pool *pgxpool.Pool, uses int) string {
	t.Helper()
	code, err := db.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateInviteCode(context.Background(), pool, code, "apple test", uses, nil); err != nil {
		t.Fatal(err)
	}
	return code
}

func redemptions(t *testing.T, pool *pgxpool.Pool, code string) int {
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT redemptions FROM invite_codes WHERE code = $1`, db.NormalizeInviteCode(code)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countRows(t *testing.T, pool *pgxpool.Pool, q string, args ...any) int {
	var n int
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Apple's form_post is cross-site, so a Lax cookie would never come back.
func TestAppleLoginHandshakeCookieSurvivesCrossSitePost(t *testing.T) {
	f := newAppleFixture(t, false)
	hc, nonce, rec := f.startWeb(t, "")
	if rec.Code != http.StatusFound || hc == nil {
		t.Fatalf("status %d, cookie %v", rec.Code, hc)
	}
	if hc.SameSite != http.SameSiteNoneMode || !hc.Secure || !hc.HttpOnly || hc.Path != "/api/auth/apple" {
		t.Fatalf("handshake cookie = %+v, want SameSite=None; Secure; HttpOnly; Path=/api/auth/apple", hc)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if loc.Host != "appleid.apple.com" || q.Get("client_id") != appleServices ||
		q.Get("response_mode") != "form_post" || q.Get("redirect_uri") != appleSite+AppleCallbackPath ||
		!strings.Contains(q.Get("scope"), "name") || !strings.Contains(q.Get("scope"), "email") {
		t.Fatalf("authorize URL = %s", loc)
	}
	// Apple sees only the hash; the raw nonce stays in the handshake.
	if q.Get("nonce") != apple.HashNonce(nonce) || q.Get("nonce") == nonce {
		t.Fatalf("authorize nonce %q is not the hash of the stored nonce", q.Get("nonce"))
	}
}

func TestAppleWebSignupStoresNameEmailAndToken(t *testing.T) {
	f := newAppleFixture(t, true)
	code := newInvite(t, f.pool, 1)
	hc, nonce, _ := f.startWeb(t, code)

	rec := f.callback(hc, url.Values{
		"state":    {f.stateFor(t, hc)},
		"code":     {"auth-code"},
		"id_token": {f.idToken(appleServices, nonce, nil)},
		// The browser posts this field and can put anything in it; only the
		// signed token's email may be stored.
		"user": {`{"name":{"firstName":"Ada","lastName":"Lovelace"},"email":"victim@example.com"}`},
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("status %d, location %q, body %q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName {
			session = c
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("no session cookie")
	}
	su, err := db.GetSessionUserByTokenHash(context.Background(), f.pool, HashSessionToken(session.Value))
	if err != nil {
		t.Fatal(err)
	}
	if su.User.DisplayName != "Ada Lovelace" || su.User.Email != "person@privaterelay.appleid.com" || su.User.SpotifyUserID != "" {
		t.Fatalf("user = %+v", su.User)
	}
	if len(su.User.AuthMethods) != 1 || su.User.AuthMethods[0] != "apple" {
		t.Fatalf("auth_methods = %v", su.User.AuthMethods)
	}
	if redemptions(t, f.pool, code) != 1 {
		t.Fatal("invite not redeemed exactly once")
	}
	var clientID string
	var ct []byte
	if err := f.pool.QueryRow(context.Background(),
		`SELECT client_id, encrypted_refresh_token FROM apple_tokens WHERE user_id = $1`, su.User.ID).Scan(&clientID, &ct); err != nil {
		t.Fatalf("apple token not stored: %v", err)
	}
	if clientID != appleServices || strings.Contains(string(ct), "refresh-") {
		t.Fatalf("token row client %q, stored in clear: %v", clientID, strings.Contains(string(ct), "refresh-"))
	}
	if f.srv.LastTokenForm().Get("redirect_uri") != appleSite+AppleCallbackPath {
		t.Fatal("web exchange must send the authorize redirect_uri")
	}
	if len(f.logins) != 1 {
		t.Fatal("OnLoginSuccess not called")
	}
}

// Apple sends the name once. A returning sign-in carries none and must not
// erase it, and needs no invite.
func TestAppleReturningUserKeepsNameAndNeedsNoInvite(t *testing.T) {
	f := newAppleFixture(t, true)
	code := newInvite(t, f.pool, 1)
	hc, nonce, _ := f.startWeb(t, code)
	f.callback(hc, url.Values{
		"state": {f.stateFor(t, hc)}, "code": {"c1"},
		"id_token": {f.idToken(appleServices, nonce, nil)},
		"user":     {`{"name":{"firstName":"Ada","lastName":"Lovelace"}}`},
	})

	hc, nonce, _ = f.startWeb(t, "")
	rec := f.callback(hc, url.Values{
		"state": {f.stateFor(t, hc)}, "code": {"c2"},
		"id_token": {f.idToken(appleServices, nonce, nil)},
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("returning sign-in: status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	if n := countRows(t, f.pool, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("%d users, want 1", n)
	}
	var name string
	_ = f.pool.QueryRow(context.Background(), `SELECT display_name FROM users`).Scan(&name)
	if name != "Ada Lovelace" {
		t.Fatalf("name = %q after a sign-in without one", name)
	}
	if redemptions(t, f.pool, code) != 1 {
		t.Fatal("a returning sign-in spent an invite")
	}
}

func TestAppleWebSignupRefusedWithoutInvite(t *testing.T) {
	f := newAppleFixture(t, true)
	hc, nonce, _ := f.startWeb(t, "")
	rec := f.callback(hc, url.Values{
		"state": {f.stateFor(t, hc)}, "code": {"c"},
		"id_token": {f.idToken(appleServices, nonce, nil)},
		"user":     {`{"name":{"firstName":"Ada"}}`},
	})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/login?error=invite_required" {
		t.Fatalf("status %d location %q", rec.Code, loc)
	}
	if countRows(t, f.pool, `SELECT count(*) FROM users`)+countRows(t, f.pool, `SELECT count(*) FROM apple_tokens`) != 0 {
		t.Fatal("a refused signup left rows behind")
	}
}

func TestAppleWebSignupRefusedWithSpentInvite(t *testing.T) {
	f := newAppleFixture(t, true)
	code := newInvite(t, f.pool, 1)
	hc, nonce, _ := f.startWeb(t, code) // pre-check passes
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE invite_codes SET redemptions = max_redemptions WHERE code = $1`, db.NormalizeInviteCode(code)); err != nil {
		t.Fatal(err)
	}
	rec := f.callback(hc, url.Values{
		"state": {f.stateFor(t, hc)}, "code": {"c"},
		"id_token": {f.idToken(appleServices, nonce, nil)},
	})
	if loc := rec.Header().Get("Location"); loc != "/login?error=invite_invalid" {
		t.Fatalf("location %q", loc)
	}
	if countRows(t, f.pool, `SELECT count(*) FROM users`) != 0 {
		t.Fatal("user created on a spent invite")
	}
}

func TestAppleCallbackRejections(t *testing.T) {
	f := newAppleFixture(t, false)

	t.Run("no handshake cookie", func(t *testing.T) {
		_, nonce, _ := f.startWeb(t, "")
		rec := f.callback(nil, url.Values{"state": {"x"}, "code": {"c"}, "id_token": {f.idToken(appleServices, nonce, nil)}})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("state mismatch", func(t *testing.T) {
		hc, nonce, _ := f.startWeb(t, "")
		rec := f.callback(hc, url.Values{"state": {"wrong"}, "code": {"c"}, "id_token": {f.idToken(appleServices, nonce, nil)}})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("handshake replay", func(t *testing.T) {
		hc, nonce, _ := f.startWeb(t, "")
		form := url.Values{"state": {f.stateFor(t, hc)}, "code": {"c"}, "id_token": {f.idToken(appleServices, nonce, nil)}}
		if rec := f.callback(hc, form); rec.Code != http.StatusSeeOther {
			t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
		}
		if rec := f.callback(hc, form); rec.Code != http.StatusBadRequest {
			t.Fatalf("replay: status %d", rec.Code)
		}
	})
	t.Run("native token on the web callback", func(t *testing.T) {
		hc, nonce, _ := f.startWeb(t, "")
		rec := f.callback(hc, url.Values{"state": {f.stateFor(t, hc)}, "code": {"c"}, "id_token": {f.idToken(appleBundle, nonce, nil)}})
		if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=apple_failed") {
			t.Fatalf("location %q", loc)
		}
	})
	t.Run("nonce from another handshake", func(t *testing.T) {
		hc, _, _ := f.startWeb(t, "")
		rec := f.callback(hc, url.Values{"state": {f.stateFor(t, hc)}, "code": {"c"}, "id_token": {f.idToken(appleServices, "other-nonce", nil)}})
		if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=apple_failed") {
			t.Fatalf("location %q", loc)
		}
	})
	t.Run("user cancelled", func(t *testing.T) {
		hc, _, _ := f.startWeb(t, "")
		rec := f.callback(hc, url.Values{"error": {"user_cancelled_authorize"}, "state": {f.stateFor(t, hc)}})
		if loc := rec.Header().Get("Location"); loc != "/login" {
			t.Fatalf("location %q", loc)
		}
	})
}

func (f *appleFixture) native(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/apple/native", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestAppleNativeSignIn(t *testing.T) {
	f := newAppleFixture(t, false)
	rec := f.native(t, map[string]any{
		"identity_token":     f.idToken(appleBundle, "app-nonce", nil),
		"authorization_code": "native-code",
		"nonce":              "app-nonce",
		"given_name":         "Grace",
		"family_name":        "Hopper",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		SessionToken string         `json:"session_token"`
		User         map[string]any `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.SessionToken == "" {
		t.Fatalf("body %s", rec.Body.String())
	}
	// iOS decodes spotify_user_id as a non-optional String.
	if v, ok := resp.User["spotify_user_id"].(string); !ok || v != "" {
		t.Fatalf("spotify_user_id = %#v, want empty string", resp.User["spotify_user_id"])
	}
	if resp.User["display_name"] != "Grace Hopper" {
		t.Fatalf("display_name = %v", resp.User["display_name"])
	}
	form := f.srv.LastTokenForm()
	if form.Get("client_id") != appleBundle || form.Get("redirect_uri") != "" {
		t.Fatalf("native exchange form = %v", form)
	}
	var clientID string
	_ = f.pool.QueryRow(context.Background(), `SELECT client_id FROM apple_tokens`).Scan(&clientID)
	if clientID != appleBundle {
		t.Fatalf("stored client_id %q; revoke must use the bundle ID for an app sign-in", clientID)
	}
}

func TestAppleNativeRejections(t *testing.T) {
	f := newAppleFixture(t, false)
	base := func() map[string]any {
		return map[string]any{
			"identity_token":     f.idToken(appleBundle, "n", nil),
			"authorization_code": "c",
			"nonce":              "n",
		}
	}
	cases := map[string]func(map[string]any){
		"web token on the native route": func(b map[string]any) { b["identity_token"] = f.idToken(appleServices, "n", nil) },
		"nonce mismatch":                func(b map[string]any) { b["nonce"] = "other" },
		"expired": func(b map[string]any) {
			b["identity_token"] = f.idToken(appleBundle, "n", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
		},
		"code for another subject": func(b map[string]any) {
			b["identity_token"] = f.idToken(appleBundle, "n", map[string]any{"sub": "someone-else"})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := base()
			mutate(b)
			if rec := f.native(t, b); rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
	if n := countRows(t, f.pool, `SELECT count(*) FROM users`); n != 0 {
		t.Fatalf("%d users created by rejected tokens", n)
	}
	t.Run("missing nonce", func(t *testing.T) {
		b := base()
		delete(b, "nonce")
		if rec := f.native(t, b); rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d", rec.Code)
		}
	})
}

func TestAppleNativeSignupNeedsInvite(t *testing.T) {
	f := newAppleFixture(t, true)
	rec := f.native(t, map[string]any{
		"identity_token": f.idToken(appleBundle, "n", nil), "authorization_code": "c", "nonce": "n",
	})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"invite_required"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	code := newInvite(t, f.pool, 1)
	rec = f.native(t, map[string]any{
		"identity_token": f.idToken(appleBundle, "n", nil), "authorization_code": "c", "nonce": "n", "invite": code,
	})
	if rec.Code != http.StatusOK || redemptions(t, f.pool, code) != 1 {
		t.Fatalf("with invite: status %d, redemptions %d", rec.Code, redemptions(t, f.pool, code))
	}
}
