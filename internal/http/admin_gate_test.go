package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
)

// The admin gate, tested against a real session and a real is_admin column.
//
// This costs a database and a second copy of the testPool helper that
// internal/db already has, and both are deliberate. The alternative was to
// export something from internal/auth that stashes a db.User on a context so a
// test could fake one -- which would put a "pretend to be this user" function
// in the same package as the middleware that trusts it. A duplicated 25-line
// test helper is the cheaper of the two risks.
func adminTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	// Migrations are the schema's source of truth here too, so this cannot
	// drift from production the way a hand-written fixture would.
	if err := db.Migrate(ctx, pool, "../../migrations"); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newSignedInUser creates a user and a live session, and returns the raw
// session token. admin decides whether the account carries the flag.
func newSignedInUser(t *testing.T, pool *pgxpool.Pool, admin bool) (spotifyID, token string) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	spotifyID = "spotify-gate-" + uuid.NewString()
	const insert = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, $3, $4, $5)`
	if _, err := pool.Exec(ctx, insert, id, spotifyID, "Gate Test User",
		[]byte("ciphertext"), []byte("nonce-123456")); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	if admin {
		if _, err := db.SetAdmin(ctx, pool, spotifyID, true); err != nil {
			t.Fatalf("grant admin: %v", err)
		}
	}
	token = uuid.NewString() + uuid.NewString()
	sess := db.Session{
		ID:        uuid.NewString(),
		TokenHash: auth.HashSessionToken(token),
		UserID:    id,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.CreateSession(ctx, pool, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return spotifyID, token
}

// adminRouter builds the real /admin subtree: the same RequireUser the server
// mounts, and AdminHandler.Mount, which installs RequireAdmin itself.
//
// CSRF is deliberately left out. It is part of the chain in production, but
// including it here would make an unauthorised POST answer 403 for the CSRF
// token rather than for the admin flag -- a green test that proves the wrong
// thing.
func adminRouter(pool *pgxpool.Pool) chi.Router {
	r := chi.NewRouter()
	// A limiter generous enough that it never fires; the bucket is not what
	// is under test here.
	limiter := auth.NewUserRateLimit(1000, 1000)
	r.Route("/admin", func(r chi.Router) {
		r.Use(auth.RequireUser(pool))
		(&AdminHandler{Pool: pool}).Mount(r, limiter)
	})
	return r
}

// adminRoutes enumerates every route the subtree actually registers, with
// path parameters filled in.
//
// Walking the router rather than listing the routes by hand is the entire
// point of this test. The failure it guards against is a route added later
// that nobody remembers to gate, and a hand-written list of URLs would not
// cover a route that did not exist when the list was written -- which is
// exactly the route at risk.
func adminRoutes(t *testing.T, r chi.Router) [][2]string {
	t.Helper()
	var out [][2]string
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// chi renders a trailing "/" on some patterns; a concrete request
		// needs the parameters substituted for something a handler will parse.
		path := strings.ReplaceAll(route, "/*", "")
		path = strings.ReplaceAll(path, "{code}", "CF-TEST-CODE")
		out = append(out, [2]string{method, path})
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no admin routes were registered; this test would pass vacuously")
	}
	return out
}

func request(t *testing.T, r chi.Router, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// The story's first acceptance clause: a signed-in NON-admin gets 403 from
// EVERY /api/admin/* route.
//
// "Every" is measured by walking the router, so a route added after this test
// was written is covered by it. What is behind these routes is an invite mint,
// and an ungated one is an unbounded signup path -- the exact hole the invite
// gate exists to close.
func TestEverySignedInNonAdminRouteIsForbidden(t *testing.T) {
	pool := adminTestPool(t)
	r := adminRouter(pool)
	_, token := newSignedInUser(t, pool, false)

	for _, rt := range adminRoutes(t, r) {
		method, path := rt[0], rt[1]
		t.Run(method+" "+path, func(t *testing.T) {
			rec := request(t, r, method, path, token)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403 for a signed-in non-admin", method, path, rec.Code)
			}
		})
	}
}

// The gate must also fail closed for a request with no session at all, and
// with the status that says so rather than the one that says "not yours".
func TestEveryAdminRouteRefusesAnonymousCallers(t *testing.T) {
	pool := adminTestPool(t)
	r := adminRouter(pool)

	for _, rt := range adminRoutes(t, r) {
		method, path := rt[0], rt[1]
		t.Run(method+" "+path, func(t *testing.T) {
			rec := request(t, r, method, path, "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s = %d, want 401 with no session", method, path, rec.Code)
			}
		})
	}
}

// The second clause: an admin can mint a code. The gate is only worth having
// if it also opens.
func TestAdminCanMintAndListAndDisable(t *testing.T) {
	pool := adminTestPool(t)
	r := adminRouter(pool)
	_, token := newSignedInUser(t, pool, true)

	rec := request(t, r, http.MethodPost, "/admin/invites", token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint = %d (%s), want 201", rec.Code, rec.Body.String())
	}
	var minted adminInvite
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode minted invite: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM invite_codes WHERE code = $1`, minted.Code)
	})
	if minted.Code == "" {
		t.Fatal("mint returned an empty code")
	}
	if minted.State != db.InviteUsable {
		t.Fatalf("a freshly minted code is %q, want %q", minted.State, db.InviteUsable)
	}

	// It shows up in the listing the console renders.
	rec = request(t, r, http.MethodGet, "/admin/invites", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: this payload is a list of live invite codes",
			rec.Header().Get("Cache-Control"))
	}
	var listed struct {
		Invites []adminInvite `json:"invites"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if !containsCode(listed.Invites, minted.Code) {
		t.Fatal("the minted code is missing from the listing")
	}

	// And it can be revoked, which must change its state rather than remove it
	// -- a spent code has to stay readable to explain where a user came from.
	rec = request(t, r, http.MethodPost, "/admin/invites/"+minted.Code+"/disable", token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	rec = request(t, r, http.MethodGet, "/admin/invites", token)
	var after struct {
		Invites []adminInvite `json:"invites"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	got := findCode(after.Invites, minted.Code)
	if got == nil {
		t.Fatal("a disabled code disappeared from the listing; it must stay readable")
	}
	if got.State != db.InviteDisabled {
		t.Fatalf("state after disable = %q, want %q", got.State, db.InviteDisabled)
	}
}

// The third clause: a code minted through the API admits exactly one signup.
// The CLI already defaults to single-use; this asserts the API did not
// introduce a second, more generous default beside it.
func TestAPIMintedCodeIsSingleUseByDefault(t *testing.T) {
	pool := adminTestPool(t)
	r := adminRouter(pool)
	_, token := newSignedInUser(t, pool, true)

	rec := request(t, r, http.MethodPost, "/admin/invites", token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint = %d (%s), want 201", rec.Code, rec.Body.String())
	}
	var minted adminInvite
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM invite_codes WHERE code = $1`, minted.Code)
	})
	if minted.MaxRedemptions != 1 {
		t.Fatalf("max_redemptions = %d, want 1", minted.MaxRedemptions)
	}

	// Read it back out of the database rather than trusting the response, so
	// this asserts what was stored and not what was echoed.
	var stored int
	if err := pool.QueryRow(context.Background(),
		`SELECT max_redemptions FROM invite_codes WHERE code = $1`, minted.Code).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != 1 {
		t.Fatalf("stored max_redemptions = %d, want 1", stored)
	}
}

// An explicit `uses` is honoured, and a nonsensical one is refused rather than
// coerced. 0 is the case that matters: coercing it to 1 would mean a request
// that plainly asked for something impossible got a working code back.
func TestMintRejectsOutOfRangeUses(t *testing.T) {
	pool := adminTestPool(t)
	r := adminRouter(pool)
	_, token := newSignedInUser(t, pool, true)

	for _, body := range []string{`{"uses":0}`, `{"uses":-1}`, `{"uses":51}`, `{"expires_days":-1}`} {
		req := httptest.NewRequest(http.MethodPost, "/admin/invites", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("mint %s = %d, want 400", body, rec.Code)
		}
	}
}

func findCode(in []adminInvite, code string) *adminInvite {
	for i := range in {
		if in[i].Code == code {
			return &in[i]
		}
	}
	return nil
}

func containsCode(in []adminInvite, code string) bool { return findCode(in, code) != nil }
