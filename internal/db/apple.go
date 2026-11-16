package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AppleSignIn is one verified Sign in with Apple authorization (CF-35),
// already checked by apple.VerifyIdentityToken and with its refresh token
// already encrypted.
type AppleSignIn struct {
	// Subject is the identity token's sub, user_identities.subject.
	Subject string
	// Email is the token's email claim, possibly a private relay address.
	Email string
	// DisplayName is the name Apple shares on the first authorization only.
	DisplayName string
	// ClientID is the client that obtained the refresh token -- the
	// Services ID on the web, the bundle ID in the app -- and the only one
	// that can revoke it.
	ClientID              string
	EncryptedRefreshToken []byte
	RefreshTokenNonce     []byte
}

// AdmitApple is the Apple provider's admission, in one transaction: find the
// identity, or create the account behind the invite gate. The redemption,
// the user, the identity and the stored refresh token commit together or not
// at all (docs/admission-policy.md), so a refused signup spends no code and a
// crash cannot leave an account whose token we never stored -- and therefore
// could never revoke.
//
// Name and email are written on creation, from this first authorization,
// because Apple sends the name exactly once. A returning user only fills
// fields that are still empty: the name never comes again, but an account
// created without one (or without the email scope) can pick the email up
// later, and an existing value is the user's and is not overwritten.
//
// The email is NOT used to find an account. Only an apple identity counts;
// accounts are never linked by address (docs/public-path.md section 3.4).
func AdmitApple(ctx context.Context, pool *pgxpool.Pool, s AppleSignIn, invite string, requireInvite bool) (uuid.UUID, bool, error) {
	if s.Subject == "" || s.ClientID == "" || len(s.EncryptedRefreshToken) == 0 {
		return uuid.Nil, false, errors.New("admit apple: subject, client and refresh token are required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID uuid.UUID
	created := false
	err = tx.QueryRow(ctx, `
SELECT user_id FROM user_identities WHERE provider = 'apple' AND subject = $1 FOR UPDATE`, s.Subject).Scan(&userID)
	switch {
	case err == nil:
		if _, err := tx.Exec(ctx, `
UPDATE user_identities SET last_login_at = now() WHERE provider = 'apple' AND subject = $1`, s.Subject); err != nil {
			return uuid.Nil, false, fmt.Errorf("touch apple identity: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE users SET
  display_name = CASE WHEN COALESCE(display_name, '') = '' THEN $2 ELSE display_name END,
  email        = CASE WHEN COALESCE(email, '') = '' THEN NULLIF($3, '') ELSE email END,
  updated_at   = now()
WHERE id = $1`, userID, s.DisplayName, s.Email); err != nil {
			return uuid.Nil, false, fmt.Errorf("fill apple profile: %w", err)
		}
	case errors.Is(err, pgx.ErrNoRows):
		invite = NormalizeInviteCode(invite)
		invitedWith := ""
		if requireInvite {
			if invite == "" {
				return uuid.Nil, false, ErrInviteRequired
			}
			if err := redeemInviteTx(ctx, tx, invite); err != nil {
				return uuid.Nil, false, err
			}
			invitedWith = invite
		}
		userID = uuid.New()
		created = true
		// No Spotify identity: NULL spotify_user_id and the empty credential
		// AccessTokenFor already answers with ErrSpotifyDisconnected.
		if _, err := tx.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce, email, invited_with)
VALUES ($1, NULL, $2, ''::bytea, ''::bytea, NULLIF($3, ''), NULLIF($4, ''))`,
			userID, s.DisplayName, s.Email, invitedWith); err != nil {
			return uuid.Nil, false, fmt.Errorf("insert apple user: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO user_identities (provider, subject, user_id) VALUES ('apple', $1, $2)`, s.Subject, userID); err != nil {
			return uuid.Nil, false, fmt.Errorf("insert apple identity: %w", err)
		}
	default:
		return uuid.Nil, false, fmt.Errorf("apple admission lookup: %w", err)
	}

	// The newest token replaces the last: it is the one Apple will honour,
	// and its client_id is the one revocation must use.
	if _, err := tx.Exec(ctx, `
INSERT INTO apple_tokens (user_id, client_id, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE SET
  client_id = EXCLUDED.client_id,
  encrypted_refresh_token = EXCLUDED.encrypted_refresh_token,
  refresh_token_nonce = EXCLUDED.refresh_token_nonce,
  updated_at = now()`, userID, s.ClientID, s.EncryptedRefreshToken, s.RefreshTokenNonce); err != nil {
		return uuid.Nil, false, fmt.Errorf("store apple token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, false, fmt.Errorf("commit apple admission: %w", err)
	}
	return userID, created, nil
}

// DeleteUser deletes an account, cascading through every table with an ON
// DELETE CASCADE foreign key to users -- and first moves any Apple refresh
// token into apple_revocations, in the same transaction, so deleting the
// account cannot lose the one value needed to revoke it at Apple.
//
// enqueue, when non-nil and an Apple token existed, is called inside the
// transaction with the queue row's id; it is where the caller inserts the
// job that performs the revoke (river's InsertTx), so the row and its job
// commit together. An error from it rolls the whole deletion back.
func DeleteUser(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, enqueue func(tx pgx.Tx, revocationID uuid.UUID) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	revID := uuid.New()
	tag, err := tx.Exec(ctx, `
INSERT INTO apple_revocations (id, client_id, encrypted_refresh_token, refresh_token_nonce)
SELECT $1, client_id, encrypted_refresh_token, refresh_token_nonce FROM apple_tokens WHERE user_id = $2`,
		revID, userID)
	if err != nil {
		return fmt.Errorf("queue apple revocation: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() > 0 && enqueue != nil {
		if err := enqueue(tx, revID); err != nil {
			return fmt.Errorf("enqueue apple revocation: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// AppleRevocation is one queued revoke.
type AppleRevocation struct {
	ID                    uuid.UUID
	ClientID              string
	EncryptedRefreshToken []byte
	RefreshTokenNonce     []byte
	Attempts              int
}

// GetAppleRevocation reads a queued revoke; ok is false once it is done.
func GetAppleRevocation(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (AppleRevocation, bool, error) {
	var r AppleRevocation
	err := pool.QueryRow(ctx, `
SELECT id, client_id, encrypted_refresh_token, refresh_token_nonce, attempts
FROM apple_revocations WHERE id = $1`, id).Scan(&r.ID, &r.ClientID, &r.EncryptedRefreshToken, &r.RefreshTokenNonce, &r.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return AppleRevocation{}, false, nil
	}
	if err != nil {
		return AppleRevocation{}, false, fmt.Errorf("get apple revocation: %w", err)
	}
	return r, true, nil
}

// CompleteAppleRevocation removes a revoke Apple has accepted.
func CompleteAppleRevocation(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	_, err := pool.Exec(ctx, `DELETE FROM apple_revocations WHERE id = $1`, id)
	return err
}

// RecordAppleRevocationFailure counts a failed attempt and keeps the reason,
// so a row left behind after the job gives up says why.
func RecordAppleRevocationFailure(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, reason string) error {
	_, err := pool.Exec(ctx, `
UPDATE apple_revocations SET attempts = attempts + 1, last_error = $2 WHERE id = $1`, id, reason)
	return err
}
