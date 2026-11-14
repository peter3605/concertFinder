package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/testdb"
)

// CF-32: SPOTIFY_MAX_USERS. A database of its own, because the cap counts
// every Spotify account in the table and the shared test database holds
// whatever the other packages left there.

func spotifyUser(id string) db.User {
	return db.User{
		SpotifyUserID:         id,
		DisplayName:           "CF-32",
		EncryptedRefreshToken: []byte("ciphertext"),
		RefreshTokenNonce:     []byte("nonce-123456"),
	}
}

func redemptions(t *testing.T, pool *pgxpool.Pool, code string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT redemptions FROM invite_codes WHERE code = $1`, code).Scan(&n); err != nil {
		t.Fatalf("read redemptions: %v", err)
	}
	return n
}

func TestSpotifyCapRefusesNewSignupsBeforeSpendingAnInvite(t *testing.T) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()
	const limit = 2

	invite, err := db.CreateInviteCode(ctx, pool, "CF-CAPT-"+uuid.NewString()[:4], "cf32", 10, nil)
	if err != nil {
		t.Fatalf("mint invite: %v", err)
	}

	// An account with no Spotify identity does not take a place.
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, NULL, 'picks only', ''::bytea, ''::bytea)`, uuid.New()); err != nil {
		t.Fatalf("insert picks user: %v", err)
	}

	for _, id := range []string{"cap-a", "cap-b"} {
		if _, err := db.UpsertUserWithAdmission(ctx, pool, spotifyUser(id), invite.Code, true, limit); err != nil {
			t.Fatalf("signup %s under the cap: %v", id, err)
		}
	}
	if got := redemptions(t, pool, invite.Code); got != 2 {
		t.Fatalf("redemptions = %d after two signups, want 2", got)
	}

	// The third is refused, and the invite it carried is untouched.
	_, err = db.UpsertUserWithAdmission(ctx, pool, spotifyUser("cap-c"), invite.Code, true, limit)
	if !errors.Is(err, db.ErrSpotifyFull) {
		t.Fatalf("signup past the cap = %v, want ErrSpotifyFull", err)
	}
	if got := redemptions(t, pool, invite.Code); got != 2 {
		t.Fatalf("redemptions = %d after a refused signup, want 2: the refusal spent a code", got)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE spotify_user_id = 'cap-c'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused signup left %d rows (err %v)", n, err)
	}

	// The cap also applies with the invite gate off: it is a separate gate.
	if _, err := db.UpsertUserWithAdmission(ctx, pool, spotifyUser("cap-d"), "", false, limit); !errors.Is(err, db.ErrSpotifyFull) {
		t.Fatalf("ungated signup past the cap = %v, want ErrSpotifyFull", err)
	}

	// A returning Spotify user is never refused -- not even when the cap has
	// since been lowered below the number already admitted, and with no code.
	for _, capNow := range []int{limit, 1, 0} {
		if _, err := db.UpsertUserWithAdmission(ctx, pool, spotifyUser("cap-a"), "", true, capNow); err != nil {
			t.Fatalf("returning user at cap %d: %v", capNow, err)
		}
	}

	// A disconnected account still holds its place: signing in again
	// restores it without passing admission.
	var aID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE spotify_user_id = 'cap-a'`).Scan(&aID); err != nil {
		t.Fatal(err)
	}
	if err := db.DisconnectSpotify(ctx, pool, aID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if got, err := db.CountSpotifyUsers(ctx, pool); err != nil || got != limit {
		t.Fatalf("CountSpotifyUsers = %d, %v after a disconnect; want %d", got, err, limit)
	}

	// NoSpotifyCap disables it.
	if _, err := db.UpsertUserWithAdmission(ctx, pool, spotifyUser("cap-e"), invite.Code, true, db.NoSpotifyCap); err != nil {
		t.Fatalf("signup with the cap disabled: %v", err)
	}
}
