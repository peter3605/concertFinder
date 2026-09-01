package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DisconnectSpotify's whole value is in what it does and does not remove, and
// every one of those choices is invisible from the outside: nothing errors,
// nothing logs, and the user is signed out either way. These pin the split.

func TestDisconnectRevokesTheCredentialAndSpotifyDerivedData(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	id := insertTestUser(t, pool, userOptIns{email: "a@example.com", digest: true})

	insertAffinityProfile(t, pool, id)
	insertSnapshot(t, pool, id, "38.9,-77.0,50")
	insertDevice(t, pool, id, "device-token-1")
	insertSession(t, pool, id, "session-1")

	if err := DisconnectSpotify(ctx, pool, id); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	// The credential itself. Zeroed rather than NULLed, because the column is
	// NOT NULL — a non-empty value here means the grant is still usable.
	u, err := GetUserByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if len(u.EncryptedRefreshToken) != 0 || len(u.RefreshTokenNonce) != 0 {
		t.Errorf("credential survived disconnect: token=%d bytes nonce=%d bytes",
			len(u.EncryptedRefreshToken), len(u.RefreshTokenNonce))
	}

	for _, tc := range []struct {
		table string
		why   string
	}{
		{"affinity_profiles", "derived from listening data we may no longer hold"},
		{"user_devices", "a push token against an account that can notify nothing"},
		{"sessions", "the disconnect must take effect on every signed-in client"},
	} {
		if n := countForUser(t, pool, tc.table, id); n != 0 {
			t.Errorf("%s: %d rows survived disconnect (%s)", tc.table, n, tc.why)
		}
	}
}

// The one that is not obvious. FanoutSendDigestWorker selects on
// digest_opt_in and a non-empty email with no session or connection check, so
// a surviving snapshot means a disconnected user keeps receiving a nightly
// digest built from the Spotify profile they just revoked. SendDigestWorker
// returns nil the moment GetConcertSnapshot misses, so deleting the snapshot
// is what actually stops the mail.
func TestDisconnectStopsTheNightlyDigest(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// Still opted in, and still has an address: this user is exactly who the
	// digest fanout selects.
	id := insertTestUser(t, pool, userOptIns{email: "a@example.com", digest: true})
	const locKey = "38.9,-77.0,50"
	insertSnapshot(t, pool, id, locKey)

	if _, hit, err := GetConcertSnapshot(ctx, pool, id, locKey); err != nil || !hit {
		t.Fatalf("precondition: snapshot should exist (hit=%v err=%v)", hit, err)
	}

	if err := DisconnectSpotify(ctx, pool, id); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	_, hit, err := GetConcertSnapshot(ctx, pool, id, locKey)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if hit {
		t.Error("snapshot survived disconnect: the digest fanout would still mail this user concerts derived from the Spotify profile they revoked")
	}
}

// The counterpart. If disconnecting cost the user their saves and
// subscriptions it would be account deletion with extra steps, and there
// would be no reason for it to exist separately.
func TestDisconnectKeepsWhatSigningBackInShouldRestore(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	id := insertTestUser(t, pool, userOptIns{email: "a@example.com", digest: true})

	mustExec(t, pool, `INSERT INTO user_saved_concerts (user_id, dedup_key) VALUES ($1, 'some-show')`, id)
	mustExec(t, pool, `INSERT INTO user_subscribed_artists (user_id, spotify_artist_id) VALUES ($1, 'artist-1')`, id)
	mustExec(t, pool, `INSERT INTO user_locations (user_id, latitude, longitude, radius_miles) VALUES ($1, 38.9, -77.0, 50)`, id)

	if err := DisconnectSpotify(ctx, pool, id); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	for _, table := range []string{"user_saved_concerts", "user_subscribed_artists", "user_locations"} {
		if n := countForUser(t, pool, table, id); n != 1 {
			t.Errorf("%s: %d rows after disconnect, want 1 — signing back in should restore the account, not an empty one", table, n)
		}
	}
	// The account itself must survive, or this is just DELETE /me/account.
	if _, err := GetUserByID(ctx, pool, id); err != nil {
		t.Errorf("user row did not survive disconnect: %v", err)
	}
	// Email preferences are the user's, not Spotify's. Nothing will be sent
	// while there is no snapshot, so there is no reason to reset them.
	u, err := GetUserByID(ctx, pool, id)
	if err == nil && !u.DigestOptIn {
		t.Error("digest_opt_in was cleared; preferences should survive a disconnect")
	}
}

func TestDisconnectOnAnUnknownUserReportsNoRows(t *testing.T) {
	pool := testPool(t)
	err := DisconnectSpotify(context.Background(), pool, uuid.New())
	if !errors.Is(err, ErrNoRows) {
		t.Errorf("got %v, want ErrNoRows", err)
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func countForUser(t *testing.T, pool *pgxpool.Pool, table string, id uuid.UUID) int {
	t.Helper()
	var n int
	// table is a constant from this file, never user input.
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+table+` WHERE user_id = $1`, id).Scan(&n)
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func insertAffinityProfile(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	mustExec(t, pool,
		`INSERT INTO affinity_profiles (user_id, artists, computed_at) VALUES ($1, '[]'::jsonb, now())`, id)
}

func insertSnapshot(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, locKey string) {
	t.Helper()
	err := UpsertConcertSnapshot(context.Background(), pool, ConcertSnapshot{
		UserID:      id,
		LocationKey: locKey,
		DedupKeys:   []string{"some-show"},
		ComputedAt:  time.Now(),
		Complete:    true,
	})
	if err != nil {
		t.Fatalf("upsert snapshot: %v", err)
	}
}

func insertDevice(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, token string) {
	t.Helper()
	mustExec(t, pool,
		`INSERT INTO user_devices (user_id, device_token, environment) VALUES ($1, $2, 'sandbox')`, id, token)
}

func insertSession(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, sessionID string) {
	t.Helper()
	mustExec(t, pool,
		`INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, now() + interval '14 days')`, sessionID, id)
}
