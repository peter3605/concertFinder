package db

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Email sign-in (CF-33, docs/public-path.md section 4.2). Every mail sent is
// one email_login_tokens row; see migration 0028.
const (
	// EmailLoginTokenTTL is how long a link and its code work.
	EmailLoginTokenTTL = 15 * time.Minute
	// EmailCodeMaxAttempts bounds guesses at one row's 6-digit code. Five
	// guesses at a million codes, across at most EmailLoginPerAddressHourly
	// live rows, is not a way in.
	EmailCodeMaxAttempts = 5
	// EmailLoginPerAddressHourly caps mail to one address, so the start
	// endpoint cannot be used to flood somebody's inbox.
	EmailLoginPerAddressHourly = 3
	// emailLoginRetention keeps sent rows long enough for the hourly and
	// daily counts to see them.
	emailLoginRetention = 48 * time.Hour
)

// Purposes, matching the CHECK in migration 0028.
const (
	EmailPurposeLogin  = "login"
	EmailPurposeVerify = "verify"
)

var (
	// ErrEmailTokenInvalid covers unknown, expired, already used and
	// out-of-attempts alike. Telling them apart tells a caller which links
	// existed.
	ErrEmailTokenInvalid = errors.New("email sign-in link or code is invalid or expired")
	// ErrEmailTaken is a verify for an address that is already another
	// account's email identity. Accounts are never merged by address
	// (docs/public-path.md section 3.4).
	ErrEmailTaken = errors.New("that address already signs in to another account")
)

// EmailLoginToken is one row of email_login_tokens. The token and code
// themselves are never held here, only their hashes.
type EmailLoginToken struct {
	TokenHash    string
	CodeHash     string
	Email        string
	Purpose      string
	UserID       uuid.UUID // uuid.Nil for a login
	InviteCode   string
	AppChallenge string
	ExpiresAt    time.Time
}

// EmailCodeHash is what code_hash stores for a code: salted with the row's
// token hash so equal codes on two rows do not share a hash. It is the only
// thing allowed to produce that value.
func EmailCodeHash(tokenHash, code string) string {
	sum := sha256.Sum256([]byte(tokenHash + "|" + code))
	return hex.EncodeToString(sum[:])
}

// InsertEmailLoginToken records a mail about to be sent.
func InsertEmailLoginToken(ctx context.Context, pool *pgxpool.Pool, t EmailLoginToken) error {
	var userID *uuid.UUID
	if t.UserID != uuid.Nil {
		userID = &t.UserID
	}
	const q = `
INSERT INTO email_login_tokens (token_hash, code_hash, email, purpose, user_id, invite_code, app_challenge, expires_at)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8)`
	if _, err := pool.Exec(ctx, q, t.TokenHash, t.CodeHash, t.Email, t.Purpose, userID,
		NormalizeInviteCode(t.InviteCode), t.AppChallenge, t.ExpiresAt); err != nil {
		return fmt.Errorf("insert email login token: %w", err)
	}
	return nil
}

// CountEmailLoginTokensSince counts mail sent to one address since a time.
func CountEmailLoginTokensSince(ctx context.Context, pool *pgxpool.Pool, email string, since time.Time) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM email_login_tokens WHERE email = $1 AND created_at >= $2`,
		email, since).Scan(&n)
	return n, err
}

// CountAllEmailLoginTokensSince counts every sign-in mail since a time; the
// daily ceiling that protects SES reputation reads it.
func CountAllEmailLoginTokensSince(ctx context.Context, pool *pgxpool.Pool, since time.Time) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM email_login_tokens WHERE created_at >= $1`, since).Scan(&n)
	return n, err
}

// EmailIdentityExists reports whether an address already signs in to an
// account. The start handler uses it only to decide whether an unknown
// address needs an invite; its answer never reaches the response.
func EmailIdentityExists(ctx context.Context, pool *pgxpool.Pool, email string) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM user_identities WHERE provider = 'email' AND subject = $1)`,
		email).Scan(&ok)
	return ok, err
}

// PeekEmailLoginToken is the read-only check behind the GET confirmation
// page. It must never consume anything: mail security gateways fetch every
// URL in a message, so a GET that spent the token would burn the link before
// the person ever saw it.
func PeekEmailLoginToken(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (purpose string, ok bool, err error) {
	err = pool.QueryRow(ctx, `
SELECT purpose FROM email_login_tokens
WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()`, tokenHash).Scan(&purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("peek email login token: %w", err)
	}
	return purpose, true, nil
}

// EmailLoginResult is what consuming a token did.
type EmailLoginResult struct {
	Token  EmailLoginToken
	UserID uuid.UUID
	// Created is true when this consumption admitted a new account.
	Created bool
}

// ConsumeEmailLoginToken spends a link token and applies it, in one
// transaction: sign in (admitting the address if it is new) or verify an
// address. accept, if non-nil, sees the row before anything is applied, and
// an error from it rolls everything back with the token still usable.
//
// requireInvite is the INVITE_REQUIRED gate, passed explicitly for the same
// reason UpsertUserWithAdmission takes it.
func ConsumeEmailLoginToken(ctx context.Context, pool *pgxpool.Pool, tokenHash string, requireInvite bool, accept func(EmailLoginToken) error) (EmailLoginResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return EmailLoginResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The guard is in the WHERE clause, so two concurrent POSTs of one link
	// cannot both match: single use without a read-then-write.
	const q = `
UPDATE email_login_tokens SET consumed_at = now()
WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING ` + emailTokenColumns
	t, err := scanEmailToken(tx.QueryRow(ctx, q, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailLoginResult{}, ErrEmailTokenInvalid
	}
	if err != nil {
		return EmailLoginResult{}, fmt.Errorf("consume email login token: %w", err)
	}
	return applyEmailToken(ctx, tx, t, requireInvite, accept)
}

// ConsumeEmailLoginCode is ConsumeEmailLoginToken for the 6-digit code,
// which arrives with the address rather than the token. Every live row of the
// given purpose for that address is a candidate, because somebody who asked
// twice may type the code from either mail. A miss charges an attempt to
// every candidate, and a row past EmailCodeMaxAttempts is no longer one.
//
// For a verify, userID narrows the candidates to the signed-in account's own
// rows; for a login it is ignored.
func ConsumeEmailLoginCode(ctx context.Context, pool *pgxpool.Pool, email, code, purpose string, userID uuid.UUID, requireInvite bool, accept func(EmailLoginToken) error) (EmailLoginResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return EmailLoginResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := `
SELECT ` + emailTokenColumns + ` FROM email_login_tokens
WHERE email = $1 AND purpose = $2 AND consumed_at IS NULL AND expires_at > now() AND attempts < $3`
	args := []any{email, purpose, EmailCodeMaxAttempts}
	if purpose == EmailPurposeVerify {
		q += ` AND user_id = $4`
		args = append(args, userID)
	}
	q += ` ORDER BY created_at DESC FOR UPDATE`
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return EmailLoginResult{}, fmt.Errorf("find email login code: %w", err)
	}
	var candidates []EmailLoginToken
	for rows.Next() {
		t, err := scanEmailToken(rows)
		if err != nil {
			rows.Close()
			return EmailLoginResult{}, err
		}
		candidates = append(candidates, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return EmailLoginResult{}, err
	}

	var match *EmailLoginToken
	for i := range candidates {
		want := candidates[i].CodeHash
		got := EmailCodeHash(candidates[i].TokenHash, code)
		if subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1 {
			match = &candidates[i]
			break
		}
	}
	if match == nil {
		if len(candidates) > 0 {
			hashes := make([]string, len(candidates))
			for i, c := range candidates {
				hashes[i] = c.TokenHash
			}
			// Committed even though the answer is a refusal: the attempt
			// has to count, or the cap bounds nothing.
			if _, err := tx.Exec(ctx,
				`UPDATE email_login_tokens SET attempts = attempts + 1 WHERE token_hash = ANY($1)`,
				hashes); err != nil {
				return EmailLoginResult{}, fmt.Errorf("charge code attempt: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return EmailLoginResult{}, err
			}
		}
		return EmailLoginResult{}, ErrEmailTokenInvalid
	}
	if _, err := tx.Exec(ctx,
		`UPDATE email_login_tokens SET consumed_at = now() WHERE token_hash = $1`, match.TokenHash); err != nil {
		return EmailLoginResult{}, fmt.Errorf("consume email login code: %w", err)
	}
	return applyEmailToken(ctx, tx, *match, requireInvite, accept)
}

func applyEmailToken(ctx context.Context, tx pgx.Tx, t EmailLoginToken, requireInvite bool, accept func(EmailLoginToken) error) (EmailLoginResult, error) {
	if accept != nil {
		if err := accept(t); err != nil {
			return EmailLoginResult{}, err
		}
	}
	res := EmailLoginResult{Token: t}
	var err error
	switch t.Purpose {
	case EmailPurposeLogin:
		res.UserID, res.Created, err = admitEmailTx(ctx, tx, t.Email, t.InviteCode, requireInvite)
	case EmailPurposeVerify:
		res.UserID = t.UserID
		err = verifyEmailTx(ctx, tx, t.UserID, t.Email)
	default:
		err = fmt.Errorf("email login token: unknown purpose %q", t.Purpose)
	}
	if err != nil {
		return EmailLoginResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EmailLoginResult{}, fmt.Errorf("commit email login: %w", err)
	}
	return res, nil
}

// admitEmailTx is the email provider's admission: find the identity, or
// create the account behind the invite gate. The redemption and the insert
// share the caller's transaction -- the rule docs/admission-policy.md sets --
// so the code is spent if and only if the user exists, and if the token is
// consumed.
//
// An address that matches some Spotify user's users.email is NOT linked to
// that account. Only an email identity counts; addresses Spotify supplied are
// not proof of mailbox ownership (docs/public-path.md section 3.4).
func admitEmailTx(ctx context.Context, tx pgx.Tx, email, invite string, requireInvite bool) (uuid.UUID, bool, error) {
	var userID uuid.UUID
	err := tx.QueryRow(ctx, `
SELECT user_id FROM user_identities WHERE provider = 'email' AND subject = $1 FOR UPDATE`, email).Scan(&userID)
	switch {
	case err == nil:
		if _, err := tx.Exec(ctx, `
UPDATE user_identities SET last_login_at = now() WHERE provider = 'email' AND subject = $1`, email); err != nil {
			return uuid.Nil, false, fmt.Errorf("touch email identity: %w", err)
		}
		return userID, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, false, fmt.Errorf("email admission lookup: %w", err)
	}

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
	// No Spotify identity: spotify_user_id NULL and the empty credential a
	// disconnected user also has, which AccessTokenFor already answers with
	// ErrSpotifyDisconnected (docs/public-path.md section 3.1). The address
	// is verified by construction, so it is stored as the account's email.
	if _, err := tx.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce, email, invited_with)
VALUES ($1, NULL, '', ''::bytea, ''::bytea, $2, NULLIF($3, ''))`, userID, email, invitedWith); err != nil {
		return uuid.Nil, false, fmt.Errorf("insert email user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO user_identities (provider, subject, user_id) VALUES ('email', $1, $2)`, email, userID); err != nil {
		return uuid.Nil, false, fmt.Errorf("insert email identity: %w", err)
	}
	return userID, true, nil
}

// verifyEmailTx confirms an address for a signed-in account: it becomes the
// account's email and its email identity, replacing any earlier one, so the
// account can sign in by email from now on.
func verifyEmailTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, email string) error {
	var owner uuid.UUID
	err := tx.QueryRow(ctx, `
SELECT user_id FROM user_identities WHERE provider = 'email' AND subject = $1 FOR UPDATE`, email).Scan(&owner)
	switch {
	case err == nil && owner != userID:
		return ErrEmailTaken
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("verify email lookup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO user_identities (provider, subject, user_id) VALUES ('email', $1, $2)
ON CONFLICT (user_id, provider) DO UPDATE SET subject = EXCLUDED.subject`, email, userID); err != nil {
		return fmt.Errorf("upsert email identity: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET email = $1, updated_at = now() WHERE id = $2`, email, userID); err != nil {
		return fmt.Errorf("set verified email: %w", err)
	}
	return nil
}

// PruneEmailLoginTokens drops rows the limits no longer count.
func PruneEmailLoginTokens(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM email_login_tokens WHERE created_at < $1`,
		time.Now().Add(-emailLoginRetention))
	return tag.RowsAffected(), err
}

const emailTokenColumns = `token_hash, code_hash, email, purpose, user_id,
       COALESCE(invite_code, ''), COALESCE(app_challenge, ''), expires_at`

func scanEmailToken(r rowScanner) (EmailLoginToken, error) {
	var t EmailLoginToken
	var userID *uuid.UUID
	if err := r.Scan(&t.TokenHash, &t.CodeHash, &t.Email, &t.Purpose, &userID,
		&t.InviteCode, &t.AppChallenge, &t.ExpiresAt); err != nil {
		return EmailLoginToken{}, err
	}
	if userID != nil {
		t.UserID = *userID
	}
	return t, nil
}
