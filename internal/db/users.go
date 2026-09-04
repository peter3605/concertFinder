package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID                    uuid.UUID
	SpotifyUserID         string
	DisplayName           string
	EncryptedRefreshToken []byte
	RefreshTokenNonce     []byte
	// Email is empty until the user grants the user-read-email scope.
	Email              string
	DigestOptIn        bool
	InstantNotifyOptIn bool
	// PushOptIn is deliberately not a reuse of InstantNotifyOptIn: "push me
	// but do not email me" is an ordinary preference. See migration 0016.
	PushOptIn bool
	// InvitedWith is the invite code that admitted this account, empty for
	// everyone who predates migration 0021 and for everyone admitted while
	// INVITE_REQUIRED was off. It is provenance, never a permission: nothing
	// reads it to decide what a user may do.
	InvitedWith string
	// IsAdmin is a permission, and the only one -- see migration 0022. It is
	// deliberately adjacent to InvitedWith so the difference is in front of
	// whoever reads either: one records how an account got in, the other
	// decides what it may do, and conflating them would make every invited
	// user an administrator.
	//
	// Every query that scans a whole User must select it. A query that omits
	// it does not fail; it returns false, which reads as "not an admin" and
	// locks the operator out of a console with nothing logged. That is why
	// sessionUserColumns carries it -- that join is what RequireUser runs on
	// every authenticated request, and it is the only source RequireAdmin
	// consults.
	IsAdmin bool
}

// GetUserByID returns the user or (User{}, pgx.ErrNoRows) if none exists.
func GetUserByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (User, error) {
	const q = `
SELECT id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce,
       COALESCE(email, ''), digest_opt_in, instant_notify_opt_in, push_opt_in,
       COALESCE(invited_with, ''), is_admin
FROM users WHERE id = $1`
	var u User
	err := pool.QueryRow(ctx, q, id).Scan(&u.ID, &u.SpotifyUserID, &u.DisplayName, &u.EncryptedRefreshToken, &u.RefreshTokenNonce, &u.Email, &u.DigestOptIn, &u.InstantNotifyOptIn, &u.PushOptIn, &u.InvitedWith, &u.IsAdmin)
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// SetDigestOptIn updates a user's digest subscription flag.
func SetDigestOptIn(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, optIn bool) error {
	const q = `UPDATE users SET digest_opt_in = $2, updated_at = now() WHERE id = $1`
	tag, err := pool.Exec(ctx, q, id, optIn)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoRows
	}
	return nil
}

// OptOutAllEmail clears every outbound-email flag at once. This is what the
// unsubscribe link has to do, and it is deliberately not SetDigestOptIn:
// instant-notify mails carry the same link under the words "Stop these
// notifications", so turning off only the digest left a user who clicked it
// still receiving instant mail, with no indication of why or what else to
// press. One link, one meaning — no more email.
func OptOutAllEmail(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	const q = `
UPDATE users
SET digest_opt_in = false, instant_notify_opt_in = false, updated_at = now()
WHERE id = $1`
	tag, err := pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoRows
	}
	return nil
}

// DisconnectSpotify severs an account's link to Spotify without deleting the
// account. This is the mechanism App Store Guideline 5.1.1(v) asks for — "a
// mechanism to revoke social network credentials and disable data access
// between the app and social network from within the app" — which until now
// only DELETE /me/account provided, and that takes the user's saves and
// subscriptions with it (plan §10.1.2).
//
// The stored credential is zeroed rather than the column made nullable: it is
// NOT NULL, an empty BYTEA satisfies that, and every reader already has to
// cope with "cannot produce an access token" because that is what a grant
// revoked at Spotify's end looks like. auth.AccessTokenFor checks for the
// empty value explicitly and returns ErrSpotifyDisconnected, which is
// required rather than tidy: gcm.Open panics on a wrong-length nonce, so
// without that check the first job or request for a disconnected user would
// crash its goroutine instead of reporting a state the user chose.
//
// Four deletions, and only two of them are obvious:
//
//   - affinity_profiles, because it is derived from Spotify listening data and
//     is the thing we are no longer entitled to hold.
//   - sessions, so the disconnect takes effect everywhere at once rather than
//     leaving a second signed-in client able to act. mobile_auth_codes
//     cascades from sessions, so pending one-time codes go with them.
//   - user_devices, so a phone does not keep a push token registered against
//     an account that can no longer produce anything to notify it about.
//   - user_concert_snapshots. This one is load-bearing rather than tidy:
//     FanoutSendDigestWorker selects users on digest_opt_in being true and the
//     email column being non-empty, with no session or connection check at
//     all, so a disconnected user whose snapshot survived would keep receiving
//     a nightly digest built
//     from the Spotify profile they just disconnected. Deleting the snapshot
//     makes SendDigestWorker's `GetConcertSnapshot` miss and return nil, which
//     is the existing quiet no-op. Nothing logs or errors in the version where
//     this delete is missing.
//
// Deliberately kept: saved concerts, subscribed artists, location, and the
// email preferences. Signing back in mints a fresh refresh token and restores
// the account rather than starting an empty one — which is the whole point of
// this existing separately from account deletion.
//
// Note what this does NOT do: it stops us using the grant, but it cannot
// revoke it at Spotify's end. Only the user can, at
// https://www.spotify.com/account/apps, and the UI has to say so rather than
// implying the connection is gone from both sides.
func DisconnectSpotify(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const clearToken = `
UPDATE users
SET encrypted_refresh_token = ''::bytea, refresh_token_nonce = ''::bytea, updated_at = now()
WHERE id = $1`
	tag, err := tx.Exec(ctx, clearToken, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoRows
	}

	for _, q := range []string{
		`DELETE FROM affinity_profiles WHERE user_id = $1`,
		`DELETE FROM user_concert_snapshots WHERE user_id = $1`,
		`DELETE FROM user_devices WHERE user_id = $1`,
		// Last, so an error above leaves the user still signed in and able to
		// try again rather than locked out of a half-disconnected account.
		`DELETE FROM sessions WHERE user_id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, id); err != nil {
			return fmt.Errorf("disconnect spotify: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// UpdateRefreshToken persists a rotated refresh token. Spotify may rotate on refresh (design §3.4).
func UpdateRefreshToken(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, ct, nonce []byte) error {
	const q = `UPDATE users SET encrypted_refresh_token = $2, refresh_token_nonce = $3, updated_at = now() WHERE id = $1`
	tag, err := pool.Exec(ctx, q, id, ct, nonce)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("user not found")
	}
	return nil
}

// ErrNoRows exposes pgx's sentinel without leaking the driver import.
var ErrNoRows = pgx.ErrNoRows
