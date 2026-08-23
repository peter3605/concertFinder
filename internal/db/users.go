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
}

// UpsertUserBySpotifyID inserts a new user or updates an existing one keyed by
// spotify_user_id. Preserves the user's digest preferences on conflict (only
// touches the fields we own here: display name, token, and — when non-empty
// — email). Returns the resulting row.
func UpsertUserBySpotifyID(ctx context.Context, pool *pgxpool.Pool, u User) (User, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	// Email is COALESCE-preserved so a re-login without the scope doesn't
	// blank out a previously-captured address.
	const q = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce, email)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
ON CONFLICT (spotify_user_id) DO UPDATE SET
  display_name            = EXCLUDED.display_name,
  encrypted_refresh_token = EXCLUDED.encrypted_refresh_token,
  refresh_token_nonce     = EXCLUDED.refresh_token_nonce,
  email                   = COALESCE(EXCLUDED.email, users.email),
  updated_at              = now()
RETURNING id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce,
          COALESCE(email, ''), digest_opt_in, instant_notify_opt_in, push_opt_in
`
	row := pool.QueryRow(ctx, q, u.ID, u.SpotifyUserID, u.DisplayName, u.EncryptedRefreshToken, u.RefreshTokenNonce, u.Email)
	var out User
	if err := row.Scan(&out.ID, &out.SpotifyUserID, &out.DisplayName, &out.EncryptedRefreshToken, &out.RefreshTokenNonce, &out.Email, &out.DigestOptIn, &out.InstantNotifyOptIn, &out.PushOptIn); err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	return out, nil
}

// GetUserByID returns the user or (User{}, pgx.ErrNoRows) if none exists.
func GetUserByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (User, error) {
	const q = `
SELECT id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce,
       COALESCE(email, ''), digest_opt_in, instant_notify_opt_in, push_opt_in
FROM users WHERE id = $1`
	var u User
	err := pool.QueryRow(ctx, q, id).Scan(&u.ID, &u.SpotifyUserID, &u.DisplayName, &u.EncryptedRefreshToken, &u.RefreshTokenNonce, &u.Email, &u.DigestOptIn, &u.InstantNotifyOptIn, &u.PushOptIn)
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
