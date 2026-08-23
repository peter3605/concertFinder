package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Session struct {
	ID         string
	UserID     uuid.UUID
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// SessionUser is a session plus the user it belongs to, resolved in one
// query. Auth middleware needs both on every request, and two round trips
// per request adds up when the frontend polls /me/concerts every 10s.
type SessionUser struct {
	Session Session
	User    User
}

func CreateSession(ctx context.Context, pool *pgxpool.Pool, s Session) error {
	const q = `INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`
	_, err := pool.Exec(ctx, q, s.ID, s.UserID, s.ExpiresAt)
	return err
}

// GetSessionUser resolves a session cookie to both the session row and its
// user in a single round trip. Returns ErrNoRows when the session is
// missing or expired.
func GetSessionUser(ctx context.Context, pool *pgxpool.Pool, sessionID string) (SessionUser, error) {
	const q = `
SELECT s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at,
       u.id, u.spotify_user_id, u.display_name,
       u.encrypted_refresh_token, u.refresh_token_nonce,
       COALESCE(u.email, ''), u.digest_opt_in, u.instant_notify_opt_in, u.push_opt_in
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id = $1 AND s.expires_at > now()
`
	var out SessionUser
	err := pool.QueryRow(ctx, q, sessionID).Scan(
		&out.Session.ID, &out.Session.UserID, &out.Session.CreatedAt,
		&out.Session.LastSeenAt, &out.Session.ExpiresAt,
		&out.User.ID, &out.User.SpotifyUserID, &out.User.DisplayName,
		&out.User.EncryptedRefreshToken, &out.User.RefreshTokenNonce,
		&out.User.Email, &out.User.DigestOptIn, &out.User.InstantNotifyOptIn, &out.User.PushOptIn,
	)
	if err != nil {
		return SessionUser{}, err
	}
	return out, nil
}

func TouchSession(ctx context.Context, pool *pgxpool.Pool, id string) error {
	const q = `UPDATE sessions SET last_seen_at = now() WHERE id = $1`
	_, err := pool.Exec(ctx, q, id)
	return err
}

func DeleteSession(ctx context.Context, pool *pgxpool.Pool, id string) error {
	const q = `DELETE FROM sessions WHERE id = $1`
	_, err := pool.Exec(ctx, q, id)
	return err
}

// PruneExpiredSessions deletes sessions past their expiry. Without this the
// table only ever grows, which matters twice over: the fanout workers scan
// it by last_seen_at every night, and every expired row is dead weight in
// that scan forever.
func PruneExpiredSessions(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	const q = `DELETE FROM sessions WHERE expires_at <= now()`
	tag, err := pool.Exec(ctx, q)
	return tag.RowsAffected(), err
}
