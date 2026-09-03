package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A local copy of auth.HashSessionToken. internal/auth imports internal/db, so
// calling the real one here would be an import cycle -- and a second spelling
// of the hash is exactly what these tests would catch, since a mismatch means
// nothing authenticates.
func sha256Hex(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// The session token is the one credential in this schema that used to be
// stored verbatim, and migration 0018 made every read of it go through a hash.
// None of that is visible to the compiler: the queries are strings, and the
// mobile half of it is a CTE whose column list has to match a shared SELECT
// fragment. A mistake in any of it is a login outage with a green build, so
// these exercise the real statements against a real Postgres.

func TestCreateSessionStoresOnlyTheHash(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "a@example.com"})

	const token = "the-raw-session-token"
	rowID := uuid.NewString()
	if err := CreateSession(ctx, pool, Session{
		ID:        rowID,
		TokenHash: sha256Hex(token),
		UserID:    user,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// The point of the change: the credential itself is not in the row, so a
	// pg_dump of this table is not a file of working logins.
	var stored string
	if err := pool.QueryRow(ctx,
		`SELECT token_hash FROM sessions WHERE id = $1`, rowID).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored == token {
		t.Fatal("the raw token was stored")
	}
	if stored != sha256Hex(token) {
		t.Fatalf("token_hash = %q, want the sha256 of the token", stored)
	}

	su, err := GetSessionUserByTokenHash(ctx, pool, sha256Hex(token))
	if err != nil {
		t.Fatalf("lookup by hash: %v", err)
	}
	if su.User.ID != user {
		t.Errorf("resolved user = %v, want %v", su.User.ID, user)
	}
	// What the middleware puts in the request context must be the opaque row
	// id, not the credential.
	if su.Session.ID != rowID {
		t.Errorf("session id = %q, want the opaque row id %q", su.Session.ID, rowID)
	}
}

// Rows written before 0018 have a NULL token_hash. They must stop resolving —
// that is what invalidates them — and, just as importantly, a caller
// presenting an empty credential must not match them.
func TestSessionsWithNoTokenHashAuthenticateNobody(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "b@example.com"})

	rowID := uuid.NewString()
	if err := CreateSession(ctx, pool, Session{
		ID: rowID, UserID: user, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create escrow session: %v", err)
	}
	// CreateSession NULLIFs the empty string; an empty string stored literally
	// would be a real value, and the unique index would then collide the
	// second time an app login escrowed a row.
	var isNull bool
	if err := pool.QueryRow(ctx,
		`SELECT token_hash IS NULL FROM sessions WHERE id = $1`, rowID).Scan(&isNull); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !isNull {
		t.Error("an empty TokenHash was stored as a value, not NULL")
	}
	if _, err := GetSessionUserByTokenHash(ctx, pool, ""); err == nil {
		t.Error("an empty credential resolved a session")
	}
}

// Two escrowed rows must coexist. The unique index on token_hash is what makes
// this worth asserting: Postgres allows many NULLs under one, but that is a
// property of the database, not of the schema as written, and it is the
// difference between two people opening the app at once and the second getting
// a 500.
func TestTwoEscrowedSessionsCoexist(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "c@example.com"})

	for i := 0; i < 2; i++ {
		if err := CreateSession(ctx, pool, Session{
			ID: uuid.NewString(), UserID: user, ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("escrow session %d: %v", i, err)
		}
	}
}

// The mobile exchange: the row is created empty at /callback and claimed once,
// which is what keeps a working token out of mobile_auth_codes.
func TestClaimSessionTokenIsSingleUse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "d@example.com"})

	rowID := uuid.NewString()
	if err := CreateSession(ctx, pool, Session{
		ID: rowID, UserID: user, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create escrow session: %v", err)
	}

	su, ok, err := ClaimSessionToken(ctx, pool, rowID, sha256Hex("minted-at-redemption"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok {
		t.Fatal("claim refused an unclaimed session")
	}
	if su.User.ID != user {
		t.Errorf("claimed user = %v, want %v", su.User.ID, user)
	}
	// The claim is what makes the session usable.
	if _, err := GetSessionUserByTokenHash(ctx, pool, sha256Hex("minted-at-redemption")); err != nil {
		t.Errorf("claimed session does not authenticate: %v", err)
	}
	// A replayed auth code must not mint a second credential for the same row.
	if _, ok, err := ClaimSessionToken(ctx, pool, rowID, sha256Hex("second-attempt")); err != nil {
		t.Fatalf("second claim errored: %v", err)
	} else if ok {
		t.Error("a session was claimed twice; a replayed auth code would mint a second live token")
	}
}

func TestDeleteSessionByTokenHashLogsThatSessionOut(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "e@example.com"})

	const token = "logout-me"
	if err := CreateSession(ctx, pool, Session{
		ID: uuid.NewString(), TokenHash: sha256Hex(token),
		UserID: user, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := DeleteSessionByTokenHash(ctx, pool, sha256Hex(token)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := GetSessionUserByTokenHash(ctx, pool, sha256Hex(token)); err == nil {
		t.Error("session still resolves after logout")
	}
}
