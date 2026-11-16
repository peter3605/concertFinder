package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/peterho/concertfinder/internal/apple"
	"github.com/peterho/concertfinder/internal/apple/appletest"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/jobs"
	"github.com/peterho/concertfinder/internal/testdb"
)

// CF-35: deleting an Apple account must revoke its grant at Apple, and a
// failed revoke must not lose the token. The token moves to
// apple_revocations and the job is inserted in the deletion's own
// transaction; the worker retries until Apple accepts.
func TestDeletingAnAppleAccountQueuesARevokeThatRetries(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Isolated(t, "../../migrations")
	encKey := make([]byte, 32)
	for i := range encKey {
		encKey[i] = byte(i)
	}

	ct, nonce, err := auth.EncryptToken(encKey, []byte("apple-refresh-token"))
	if err != nil {
		t.Fatal(err)
	}
	userID, _, err := db.AdmitApple(ctx, pool, db.AppleSignIn{
		Subject:               "001.delete-me",
		DisplayName:           "Delete Me",
		ClientID:              "com.example.app.web",
		EncryptedRefreshToken: ct,
		RefreshTokenNonce:     nonce,
	}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.NewString() + uuid.NewString()
	if err := db.CreateSession(ctx, pool, db.Session{
		ID: uuid.NewString(), TokenHash: auth.HashSessionToken(token), UserID: userID,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	rc, err := river.NewClient[pgx.Tx](riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Route("/me", func(r chi.Router) {
		r.Use(auth.RequireUser(pool))
		r.Delete("/account", (&AccountHandler{Pool: pool, River: rc}).Delete)
	})
	req := httptest.NewRequest(http.MethodDelete, "/me/account", strings.NewReader(`{"confirm_name":"delete me"}`))
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d body %s", rec.Code, rec.Body.String())
	}

	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatal("user survived deletion")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM apple_tokens`).Scan(&n)
	if n != 0 {
		t.Fatal("apple_tokens row survived the user")
	}
	var revID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM apple_revocations`).Scan(&revID); err != nil {
		t.Fatalf("no revocation queued: %v", err)
	}
	var args string
	if err := pool.QueryRow(ctx, `SELECT args::text FROM river_job WHERE kind = 'revoke_apple_token'`).Scan(&args); err != nil {
		t.Fatalf("no revoke job inserted with the deletion: %v", err)
	}
	if !strings.Contains(args, revID.String()) || strings.Contains(args, "apple-refresh-token") {
		t.Fatalf("job args %s: must name the queue row and never carry the token", args)
	}

	srv := appletest.NewServer(t)
	client, err := apple.New(apple.Config{TeamID: "T", KeyID: "K", P8Key: srv.EscapedP8, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	w := &jobs.RevokeAppleTokenWorker{Pool: pool, Apple: client, EncKey: encKey}
	job := &river.Job[jobs.RevokeAppleTokenArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   jobs.RevokeAppleTokenArgs{RevocationID: revID},
	}

	// Apple down: the job errors (river retries) and the row stays.
	srv.RevokeStatus.Store(503)
	if err := w.Work(ctx, job); err == nil {
		t.Fatal("a 503 must fail the job so river retries it")
	}
	var attempts int
	var lastErr string
	if err := pool.QueryRow(ctx, `SELECT attempts, last_error FROM apple_revocations WHERE id = $1`, revID).Scan(&attempts, &lastErr); err != nil {
		t.Fatalf("row lost after a failed revoke: %v", err)
	}
	if attempts != 1 || lastErr == "" {
		t.Fatalf("attempts %d last_error %q", attempts, lastErr)
	}

	// Apple back: revoked with the decrypted token and the issuing client.
	srv.RevokeStatus.Store(0)
	if err := w.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if got := srv.Revoked(); len(got) != 1 || got[0] != "apple-refresh-token" {
		t.Fatalf("revoked %v", got)
	}
	if got := srv.RevokedClientIDs(); got[0] != "com.example.app.web" {
		t.Fatalf("revoked with client %v, want the client that obtained the token", got)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM apple_revocations`).Scan(&n)
	if n != 0 {
		t.Fatal("revocation row not removed after success")
	}
	// Re-running a finished job is a no-op.
	if err := w.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
}

// A token Apple no longer recognises has nothing left to revoke; retrying it
// for days would only fill the logs.
func TestAppleRevokeInvalidGrantCompletes(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Isolated(t, "../../migrations")
	encKey := make([]byte, 32)
	ct, nonce, _ := auth.EncryptToken(encKey, []byte("gone"))
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO apple_revocations (id, client_id, encrypted_refresh_token, refresh_token_nonce) VALUES ($1, 'c', $2, $3)`,
		id, ct, nonce); err != nil {
		t.Fatal(err)
	}
	srv := appletest.NewServer(t)
	srv.RevokeStatus.Store(400)
	srv.RevokeError.Store("invalid_grant")
	client, _ := apple.New(apple.Config{TeamID: "T", KeyID: "K", P8Key: srv.EscapedP8, BaseURL: srv.URL})
	w := &jobs.RevokeAppleTokenWorker{Pool: pool, Apple: client, EncKey: encKey}
	if err := w.Work(ctx, &river.Job[jobs.RevokeAppleTokenArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1}, Args: jobs.RevokeAppleTokenArgs{RevocationID: id},
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM apple_revocations`).Scan(&n)
	if n != 0 {
		t.Fatal("invalid_grant should complete the revocation")
	}
}

// Deleting an account with no Apple identity queues nothing.
func TestDeletingANonAppleAccountQueuesNoRevoke(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Isolated(t, "../../migrations")
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, 'sp', 'x', ''::bytea, ''::bytea)`, id); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := db.DeleteUser(ctx, pool, id, func(pgx.Tx, uuid.UUID) error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM apple_revocations`).Scan(&n)
	if called || n != 0 {
		t.Fatalf("enqueue called %v, rows %d", called, n)
	}
}
