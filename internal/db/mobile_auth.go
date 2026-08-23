package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MobileAuthCode is the short-lived escrow between /api/auth/callback and
// POST /api/auth/mobile/exchange. It holds a session the app has not yet been
// given, plus the challenge that proves the redeemer is the app that started
// the login.
type MobileAuthCode struct {
	Code         string
	SessionID    string
	AppChallenge string
	ExpiresAt    time.Time
}

// MobileCodeTTL is how long a minted code stays redeemable. Sixty seconds is
// the whole window between ASWebAuthenticationSession handing the app a URL
// and the app POSTing it back — a human is not in this loop, so a generous
// TTL buys nothing and widens the replay window.
const MobileCodeTTL = 60 * time.Second

// PutMobileAuthCode stores a freshly minted code.
func PutMobileAuthCode(ctx context.Context, pool *pgxpool.Pool, c MobileAuthCode) error {
	const q = `INSERT INTO mobile_auth_codes (code, session_id, app_challenge, expires_at) VALUES ($1, $2, $3, $4)`
	_, err := pool.Exec(ctx, q, c.Code, c.SessionID, c.AppChallenge, c.ExpiresAt)
	return err
}

// TakeMobileAuthCode atomically deletes and returns a code, in one round
// trip. Single-use is enforced here rather than by the caller: DELETE ...
// RETURNING means a replayed code loses the race against its own first
// redemption instead of both succeeding.
//
// Returns (zero, false, nil) when the code is unknown, already burned, or
// expired — the caller must not distinguish these in its response, since
// doing so tells an attacker which codes existed.
func TakeMobileAuthCode(ctx context.Context, pool *pgxpool.Pool, code string) (MobileAuthCode, bool, error) {
	const q = `
DELETE FROM mobile_auth_codes
WHERE code = $1 AND expires_at > now()
RETURNING code, session_id, app_challenge, expires_at
`
	var c MobileAuthCode
	err := pool.QueryRow(ctx, q, code).Scan(&c.Code, &c.SessionID, &c.AppChallenge, &c.ExpiresAt)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return MobileAuthCode{}, false, nil
		}
		return MobileAuthCode{}, false, err
	}
	return c, true, nil
}

// PruneExpiredMobileAuthCodes deletes stale rows. Janitor step. Codes are
// single-use so most rows vanish on redemption; these are the abandoned
// logins.
func PruneExpiredMobileAuthCodes(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	const q = `DELETE FROM mobile_auth_codes WHERE expires_at <= now()`
	tag, err := pool.Exec(ctx, q)
	return tag.RowsAffected(), err
}
