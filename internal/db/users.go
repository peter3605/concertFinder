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
}

// UpsertUserBySpotifyID inserts a new user or updates an existing one keyed by
// spotify_user_id. Returns the resulting row (with its stable UUID).
func UpsertUserBySpotifyID(ctx context.Context, pool *pgxpool.Pool, u User) (User, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	const q = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (spotify_user_id) DO UPDATE SET
  display_name            = EXCLUDED.display_name,
  encrypted_refresh_token = EXCLUDED.encrypted_refresh_token,
  refresh_token_nonce     = EXCLUDED.refresh_token_nonce,
  updated_at              = now()
RETURNING id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce
`
	row := pool.QueryRow(ctx, q, u.ID, u.SpotifyUserID, u.DisplayName, u.EncryptedRefreshToken, u.RefreshTokenNonce)
	var out User
	if err := row.Scan(&out.ID, &out.SpotifyUserID, &out.DisplayName, &out.EncryptedRefreshToken, &out.RefreshTokenNonce); err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	return out, nil
}

// GetUserByID returns the user or (User{}, pgx.ErrNoRows) if none exists.
func GetUserByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (User, error) {
	const q = `SELECT id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce FROM users WHERE id = $1`
	var u User
	err := pool.QueryRow(ctx, q, id).Scan(&u.ID, &u.SpotifyUserID, &u.DisplayName, &u.EncryptedRefreshToken, &u.RefreshTokenNonce)
	if err != nil {
		return User{}, err
	}
	return u, nil
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
