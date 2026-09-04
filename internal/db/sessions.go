package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Session struct {
	// ID is an opaque row identifier, not a credential. It used to be the
	// session token itself; migration 0018 separated them so a database dump
	// no longer contains anything that can be presented as a login. It is
	// still the primary key, because mobile_auth_codes references it.
	ID string

	// TokenHash is sha256(token) in lowercase hex, and the only thing a
	// request is matched against. Empty means "not issued yet" and is stored
	// as NULL — the escrow state a mobile login sits in between the OAuth
	// callback and the exchange.
	TokenHash string

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

// sessionUserColumns is shared by every query that returns a SessionUser so
// the three of them cannot drift out of scan order.
// u.is_admin is here rather than left to a later lookup because this join is
// the only thing auth.RequireAdmin reads. Dropping it would not error -- the
// field would scan as false, every admin request would 403, and the console
// would be unreachable with nothing in the logs saying why.
const sessionUserColumns = `s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at,
       u.id, u.spotify_user_id, u.display_name,
       u.encrypted_refresh_token, u.refresh_token_nonce,
       COALESCE(u.email, ''), u.digest_opt_in, u.instant_notify_opt_in, u.push_opt_in,
       u.is_admin`

func scanSessionUser(row interface{ Scan(...any) error }) (SessionUser, error) {
	var out SessionUser
	err := row.Scan(
		&out.Session.ID, &out.Session.UserID, &out.Session.CreatedAt,
		&out.Session.LastSeenAt, &out.Session.ExpiresAt,
		&out.User.ID, &out.User.SpotifyUserID, &out.User.DisplayName,
		&out.User.EncryptedRefreshToken, &out.User.RefreshTokenNonce,
		&out.User.Email, &out.User.DigestOptIn, &out.User.InstantNotifyOptIn, &out.User.PushOptIn,
		&out.User.IsAdmin,
	)
	if err != nil {
		return SessionUser{}, err
	}
	return out, nil
}

// CreateSession inserts a session. An empty TokenHash is stored as NULL
// rather than ” on purpose: NULL is the escrow state (no token has been
// issued for this row yet) and `WHERE token_hash = $1` can never match it,
// whereas an empty string would be matched by a caller that hashed nothing.
func CreateSession(ctx context.Context, pool *pgxpool.Pool, s Session) error {
	const q = `INSERT INTO sessions (id, user_id, token_hash, expires_at) VALUES ($1, $2, NULLIF($3::text, ''), $4)`
	_, err := pool.Exec(ctx, q, s.ID, s.UserID, s.TokenHash, s.ExpiresAt)
	return err
}

// GetSessionUserByTokenHash resolves a presented credential to both the
// session row and its user in a single round trip. Returns ErrNoRows when
// the session is missing or expired.
//
// The argument is a hash, never the token — auth.HashSessionToken is the only
// thing that should be producing it. Sessions written before migration 0018
// have a NULL token_hash and can therefore never match, which is what
// invalidates them.
func GetSessionUserByTokenHash(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (SessionUser, error) {
	const q = `
SELECT ` + sessionUserColumns + `
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1 AND s.expires_at > now()
`
	return scanSessionUser(pool.QueryRow(ctx, q, tokenHash))
}

// ClaimSessionToken issues the token for a session that does not have one yet
// and returns the session with its user, in one statement.
//
// This is how the iOS login gets a bearer token without one ever being
// stored. /api/auth/callback creates the session with no token_hash, so it is
// unauthenticatable, and hands the app a one-time code that names the row;
// the exchange mints a token, claims the row with its hash, and returns the
// token to the app and nowhere else. Parking the raw token in
// mobile_auth_codes instead would put the credential back in the database —
// briefly, but the whole reason for migration 0018 is that "briefly" includes
// whenever the backup happens to run.
//
// `token_hash IS NULL` makes the claim single-use: a replayed exchange
// updates no row and comes back (zero, false, nil), which the caller reports
// with the same message as an unknown code.
func ClaimSessionToken(ctx context.Context, pool *pgxpool.Pool, sessionID, tokenHash string) (SessionUser, bool, error) {
	const q = `
WITH claimed AS (
  UPDATE sessions
     SET token_hash = $2, last_seen_at = now()
   WHERE id = $1 AND token_hash IS NULL AND expires_at > now()
  RETURNING id, user_id, created_at, last_seen_at, expires_at
)
SELECT ` + sessionUserColumns + `
FROM claimed s
JOIN users u ON u.id = s.user_id
`
	su, err := scanSessionUser(pool.QueryRow(ctx, q, sessionID, tokenHash))
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return SessionUser{}, false, nil
		}
		return SessionUser{}, false, err
	}
	su.Session.TokenHash = tokenHash
	return su, true, nil
}

// TouchSession refreshes last_seen_at. Keyed on the opaque row id, which the
// middleware already has from the join — never on the credential, so this
// stays a plain primary-key update.
func TouchSession(ctx context.Context, pool *pgxpool.Pool, id string) error {
	const q = `UPDATE sessions SET last_seen_at = now() WHERE id = $1`
	_, err := pool.Exec(ctx, q, id)
	return err
}

// DeleteSession removes a session by its opaque row id.
func DeleteSession(ctx context.Context, pool *pgxpool.Pool, id string) error {
	const q = `DELETE FROM sessions WHERE id = $1`
	_, err := pool.Exec(ctx, q, id)
	return err
}

// DeleteSessionByTokenHash removes the session a presented credential names.
// Logout has the token and not the row id, and resolving one to the other
// first would be a second round trip for a value the index already holds.
func DeleteSessionByTokenHash(ctx context.Context, pool *pgxpool.Pool, tokenHash string) error {
	const q = `DELETE FROM sessions WHERE token_hash = $1`
	_, err := pool.Exec(ctx, q, tokenHash)
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
