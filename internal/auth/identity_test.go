package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// Migration 0023 makes users.spotify_user_id nullable and these tests are what
// stands between that and a scan error on every authenticated request. Each
// one gets a private schema, because they need the schema at a specific
// version (or to run a down migration), which the shared test database cannot
// offer to packages running in parallel.
func identityTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	schema := "identity_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// applyUpMigrations runs the .up.sql files whose version is in [from, to].
func applyUpMigrations(t *testing.T, pool *pgxpool.Pool, from, to int) {
	t.Helper()
	files, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	sort.Strings(files)
	for _, f := range files {
		version, err := strconv.Atoi(strings.SplitN(filepath.Base(f), "_", 2)[0])
		if err != nil {
			t.Fatalf("migration %s has no version: %v", f, err)
		}
		if version < from || version > to {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(context.Background(), string(body)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
}

const allMigrations = 1 << 30

// insertRawUser writes a user directly. spotifyID nil stores NULL, and the
// empty credential is the state design section 3.1 gives a manual user.
func insertRawUser(t *testing.T, pool *pgxpool.Pool, spotifyID *string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	const q = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, 'Identity Test User', $3, $4)`
	if _, err := pool.Exec(context.Background(), q, id, spotifyID, []byte{}, []byte{}); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func newLiveSession(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) string {
	t.Helper()
	token := uuid.NewString() + uuid.NewString()
	err := db.CreateSession(context.Background(), pool, db.Session{
		ID:        uuid.NewString(),
		TokenHash: HashSessionToken(token),
		UserID:    userID,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return token
}

// getMe drives /me through the real RequireUser and returns the decoded body,
// kept as raw JSON so a field that went missing differs from one that is empty.
func getMe(t *testing.T, pool *pgxpool.Pool, token string) map[string]json.RawMessage {
	t.Helper()
	d := &Deps{Pool: pool}
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	RequireUser(pool)(http.HandlerFunc(d.handleMe)).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/me status = %d, body %q; want 200", w.Code, w.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	return out
}

// exchangeUser drives the real mobile exchange and returns its user object.
func exchangeUser(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	ctx := context.Background()
	sessionID := uuid.NewString()
	if err := db.CreateSession(ctx, pool, db.Session{
		ID: sessionID, UserID: userID, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create escrowed session: %v", err)
	}
	code, verifier := uuid.NewString(), uuid.NewString()+uuid.NewString()
	if err := db.PutMobileAuthCode(ctx, pool, db.MobileAuthCode{
		Code:         code,
		SessionID:    sessionID,
		AppChallenge: ChallengeFromVerifier(verifier),
		ExpiresAt:    time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("put code: %v", err)
	}
	body, _ := json.Marshal(mobileExchangeRequest{Code: code, Verifier: verifier})
	r := httptest.NewRequest(http.MethodPost, "/mobile/exchange", bytes.NewReader(body))
	w := httptest.NewRecorder()
	(&Deps{Pool: pool}).handleMobileExchange(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("exchange status = %d, body %q; want 200", w.Code, w.Body.String())
	}
	var out struct {
		User map[string]json.RawMessage `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode exchange: %v", err)
	}
	return out.User
}

// assertField compares a field's exact JSON. iOS decodes spotify_user_id as a
// non-optional String, so absent and null both fail every build on a phone.
func assertField(t *testing.T, where string, payload map[string]json.RawMessage, key, want string) {
	t.Helper()
	got, ok := payload[key]
	if !ok {
		t.Errorf("%s: %q is missing", where, key)
		return
	}
	if string(got) != want {
		t.Errorf("%s: %q = %s, want %s", where, key, got, want)
	}
}

func TestSessionFromBeforeIdentitiesMigrationStillResolves(t *testing.T) {
	pool := identityTestPool(t)
	ctx := context.Background()

	applyUpMigrations(t, pool, 1, 22)
	spotifyID := "spotify-before-0023"
	userID := insertRawUser(t, pool, &spotifyID)
	token := newLiveSession(t, pool, userID)
	applyUpMigrations(t, pool, 23, allMigrations)

	var provider, subject string
	var owner uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT provider, subject, user_id FROM user_identities`).
		Scan(&provider, &subject, &owner); err != nil {
		t.Fatalf("read backfilled identity: %v", err)
	}
	if provider != "spotify" || subject != spotifyID || owner != userID {
		t.Errorf("backfill = (%s, %s, %s), want (spotify, %s, %s)", provider, subject, owner, spotifyID, userID)
	}

	me := getMe(t, pool, token)
	assertField(t, "/me", me, "spotify_user_id", `"`+spotifyID+`"`)
	assertField(t, "/me", me, "auth_methods", `["spotify"]`)

	exchanged := exchangeUser(t, pool, userID)
	assertField(t, "exchange", exchanged, "spotify_user_id", `"`+spotifyID+`"`)
	assertField(t, "exchange", exchanged, "auth_methods", `["spotify"]`)
}

func TestUserWithoutSpotifyIDPassesEveryUserRead(t *testing.T) {
	pool := identityTestPool(t)
	ctx := context.Background()
	applyUpMigrations(t, pool, 1, allMigrations)

	userID := insertRawUser(t, pool, nil)
	token := newLiveSession(t, pool, userID)

	got, err := db.GetUserByID(ctx, pool, userID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.SpotifyUserID != "" {
		t.Errorf("GetUserByID SpotifyUserID = %q, want empty", got.SpotifyUserID)
	}
	su, err := db.GetSessionUserByTokenHash(ctx, pool, HashSessionToken(token))
	if err != nil {
		t.Fatalf("session join: %v", err)
	}
	if su.User.ID != userID {
		t.Errorf("session join user = %s, want %s", su.User.ID, userID)
	}

	// No identity at all must still be a list, not null.
	me := getMe(t, pool, token)
	assertField(t, "/me", me, "spotify_user_id", `""`)
	assertField(t, "/me", me, "auth_methods", `[]`)

	if _, err := pool.Exec(ctx, `
INSERT INTO user_identities (provider, subject, user_id)
VALUES ('email', 'someone@example.com', $1), ('apple', 'apple-sub', $1)`, userID); err != nil {
		t.Fatalf("add identities: %v", err)
	}
	assertField(t, "/me", getMe(t, pool, token), "auth_methods", `["apple","email"]`)

	exchanged := exchangeUser(t, pool, userID)
	assertField(t, "exchange", exchanged, "spotify_user_id", `""`)
	assertField(t, "exchange", exchanged, "auth_methods", `["apple","email"]`)
}

func TestIdentitiesDownMigrationRestoresTheConstraint(t *testing.T) {
	pool := identityTestPool(t)
	ctx := context.Background()
	// Stop at 0023: a later migration may depend on what this one creates.
	applyUpMigrations(t, pool, 1, 23)
	body, err := os.ReadFile("../../migrations/0023_user_identities.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	tableExists := func() bool {
		t.Helper()
		var name *string
		if err := pool.QueryRow(ctx, `SELECT to_regclass('user_identities')::text`).Scan(&name); err != nil {
			t.Fatalf("to_regclass: %v", err)
		}
		return name != nil
	}

	// An account the old schema cannot hold must stop the rollback whole.
	manual := insertRawUser(t, pool, nil)
	if _, err := pool.Exec(ctx, string(body)); err == nil {
		t.Fatal("down migration succeeded with a NULL spotify_user_id present")
	}
	if !tableExists() {
		t.Fatal("failed down migration dropped user_identities anyway")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, manual); err != nil {
		t.Fatalf("remove manual user: %v", err)
	}

	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	if tableExists() {
		t.Error("user_identities still exists after down migration")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, 'x', '', '')`, uuid.New()); err == nil {
		t.Error("spotify_user_id accepted NULL after down migration")
	}
}
