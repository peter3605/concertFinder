package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Database-backed. Skips without TEST_DATABASE_URL; CI provides one. ---

// insertAdminTestUser creates a user with a known spotify_user_id, since the
// admin flag is addressed by that rather than by the internal UUID.
func insertAdminTestUser(t *testing.T, pool *pgxpool.Pool, spotifyID string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	const q = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, $3, $4, $5)`
	_, err := pool.Exec(context.Background(), q,
		id, spotifyID, "Admin Test User", []byte("ciphertext"), []byte("nonce-123456"))
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// A fresh account is not an admin. This is the half of migration 0022 that
// decides whether the column is safe to add to a live database: every existing
// row, including the operator's own, has to come out of the migration without
// the flag.
func TestNewUsersAreNotAdmins(t *testing.T) {
	pool := testPool(t)
	id := insertAdminTestUser(t, pool, "spotify-fresh-"+uuid.NewString())

	u, err := GetUserByID(context.Background(), pool, id)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.IsAdmin {
		t.Fatal("a newly created user is an admin; the column's default is wrong")
	}
}

func TestSetAdminGrantsAndRevokes(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	spotifyID := "spotify-admin-" + uuid.NewString()
	id := insertAdminTestUser(t, pool, spotifyID)

	acct, err := SetAdmin(ctx, pool, spotifyID, true)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if acct.ID != id {
		t.Fatalf("granted %v, want %v", acct.ID, id)
	}
	u, err := GetUserByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !u.IsAdmin {
		t.Fatal("IsAdmin is false after a grant")
	}

	// The other direction, because a privilege that cannot be removed is a
	// one-way door and this is the only thing that proves the door swings.
	if _, err := SetAdmin(ctx, pool, spotifyID, false); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	u, err = GetUserByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.IsAdmin {
		t.Fatal("IsAdmin is true after a revoke")
	}
}

// The likely operator mistake: granting the flag to a Spotify account that has
// never signed in, so has no row. It must be a named refusal rather than a
// silent no-op, or the operator goes looking for a broken console instead of a
// missing login.
func TestSetAdminOnUnknownAccountReportsNoRows(t *testing.T) {
	pool := testPool(t)
	_, err := SetAdmin(context.Background(), pool, "spotify-nobody-"+uuid.NewString(), true)
	if !errors.Is(err, ErrNoRows) {
		t.Fatalf("err = %v, want ErrNoRows", err)
	}
}

func TestListAdminsReturnsOnlyFlaggedAccounts(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	adminID := "spotify-listed-" + uuid.NewString()
	plainID := "spotify-unlisted-" + uuid.NewString()
	insertAdminTestUser(t, pool, adminID)
	insertAdminTestUser(t, pool, plainID)
	if _, err := SetAdmin(ctx, pool, adminID, true); err != nil {
		t.Fatalf("grant: %v", err)
	}

	admins, err := ListAdmins(ctx, pool)
	if err != nil {
		t.Fatalf("list admins: %v", err)
	}
	var sawAdmin, sawPlain bool
	for _, a := range admins {
		switch a.SpotifyUserID {
		case adminID:
			sawAdmin = true
		case plainID:
			sawPlain = true
		}
	}
	if !sawAdmin {
		t.Error("the granted account is missing from ListAdmins")
	}
	if sawPlain {
		t.Error("an account without the flag appears in ListAdmins")
	}
}

// The trap this guards is invisible: RequireAdmin reads is_admin off the
// db.User that the session join produced, and sessionUserColumns is a separate
// list of columns from GetUserByID's. Drop is_admin from that const and
// nothing errors -- every admin request simply 403s forever, which looks
// exactly like not having been granted the flag.
func TestSessionJoinCarriesTheAdminFlag(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	spotifyID := "spotify-session-admin-" + uuid.NewString()
	userID := insertAdminTestUser(t, pool, spotifyID)
	if _, err := SetAdmin(ctx, pool, spotifyID, true); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// The hash is opaque to this package; any distinct value works, because
	// what is under test is which columns the join selects.
	const tokenHash = "0000000000000000000000000000000000000000000000000000000000000001"
	sess := Session{
		ID:        uuid.NewString(),
		TokenHash: tokenHash,
		UserID:    userID,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := CreateSession(ctx, pool, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	su, err := GetSessionUserByTokenHash(ctx, pool, tokenHash)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if !su.User.IsAdmin {
		t.Fatal("the session join returned IsAdmin=false for an admin; is_admin is missing from sessionUserColumns")
	}
}
