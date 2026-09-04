package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OAuthHandshake mirrors the auth-package handshake shape but stored in DB.
// See internal/auth/handshake.go for callsite semantics.
type OAuthHandshake struct {
	Key      string
	Verifier string
	State    string
	// AppChallenge is S256(app_verifier) when the login was started by the
	// iOS client, and empty for a browser login. The callback branches on it
	// to decide whether to set a session cookie and redirect to PostLoginURL,
	// or mint a one-time code and redirect to the universal link.
	AppChallenge string
	// InviteCode is the code presented at /login, carried across the round
	// trip to Spotify so /callback can redeem it. Empty when the deployment
	// has no invite gate, and empty for a login that turns out to belong to
	// an account that already exists -- that path never reads it.
	InviteCode string
	ExpiresAt  time.Time
}

// PutHandshake inserts a fresh handshake row.
func PutHandshake(ctx context.Context, pool *pgxpool.Pool, h OAuthHandshake) error {
	const q = `INSERT INTO oauth_handshakes (handshake_key, verifier, state, expires_at, app_challenge, invite_code) VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''))`
	_, err := pool.Exec(ctx, q, h.Key, h.Verifier, h.State, h.ExpiresAt, h.AppChallenge, h.InviteCode)
	return err
}

// TakeHandshake atomically deletes and returns the handshake in one round
// trip. Second callers with the same key get (zero, false).
func TakeHandshake(ctx context.Context, pool *pgxpool.Pool, key string) (OAuthHandshake, bool, error) {
	const q = `
DELETE FROM oauth_handshakes
WHERE handshake_key = $1 AND expires_at > now()
RETURNING handshake_key, verifier, state, expires_at, COALESCE(app_challenge, ''), COALESCE(invite_code, '')
`
	var h OAuthHandshake
	err := pool.QueryRow(ctx, q, key).Scan(&h.Key, &h.Verifier, &h.State, &h.ExpiresAt, &h.AppChallenge, &h.InviteCode)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return OAuthHandshake{}, false, nil
		}
		return OAuthHandshake{}, false, err
	}
	return h, true, nil
}

// PruneExpiredHandshakes deletes stale rows. Run periodically from the
// janitor; not on the hot path.
func PruneExpiredHandshakes(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	const q = `DELETE FROM oauth_handshakes WHERE expires_at <= now()`
	tag, err := pool.Exec(ctx, q)
	return tag.RowsAffected(), err
}
