package db

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InviteCode is one admission slot, or several if MaxRedemptions is above 1.
// See migration 0021 for why the code is stored in the clear.
type InviteCode struct {
	Code           string
	Note           string
	MaxRedemptions int
	Redemptions    int
	// ExpiresAt and DisabledAt are nil when unset. Pointers rather than zero
	// times because "no expiry" and "expired at the zero instant" are
	// different answers and only one of them is true.
	ExpiresAt  *time.Time
	DisabledAt *time.Time
	CreatedAt  time.Time
}

// Usable reports whether the code would be accepted right now. It mirrors
// inviteUsableSQL; the two exist separately because one runs in Postgres
// during a redemption and the other renders an operator's list, and keeping
// them side by side is what stops them drifting.
func (c InviteCode) Usable(now time.Time) bool {
	switch {
	case c.DisabledAt != nil:
		return false
	case c.ExpiresAt != nil && !c.ExpiresAt.After(now):
		return false
	case c.Redemptions >= c.MaxRedemptions:
		return false
	}
	return true
}

// ErrInviteRequired means a signup arrived with no code at all.
// ErrInviteInvalid means it carried one that is unknown, spent, expired or
// disabled.
//
// They are separate errors because they are separate sentences to the person
// reading them: one is "you need an invite", the other is "that invite does
// not work". Collapsing them into one message is how a typo becomes an
// unanswerable support question.
var (
	ErrInviteRequired = errors.New("an invite code is required to sign up")
	ErrInviteInvalid  = errors.New("invite code is not valid")
)

// inviteUsableSQL is the single predicate deciding whether a code may be
// redeemed. It is a const shared by the read-only pre-check and the redemption
// itself so that a code accepted at /login cannot be refused at /callback for
// a reason the pre-check never applied.
const inviteUsableSQL = `disabled_at IS NULL
  AND (expires_at IS NULL OR expires_at > now())
  AND redemptions < max_redemptions`

// The minted code format, and the only place that knows it.
//
// Generation used to live in cmd/server while the normalizer lived here, and
// that split is exactly how they drifted: the generator emitted dashes, the
// normalizer stripped them, and a code pasted without its dashes stopped
// matching the row it names. Both halves are here now so a change to one is
// in front of whoever changes the other.
//
// inviteAlphabet omits I, L, O, U, 0 and 1. These codes get read down a phone
// and typed off screenshots, so the characters that are indistinguishable in
// a sans-serif font are simply never minted. U is dropped as well, which is
// Crockford's convention and costs nothing.
const (
	inviteAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
	invitePrefix   = "CF"
	inviteGroups   = 2
	inviteGroupLen = 4
)

// inviteBodyLen is how many alphabet characters a code carries, excluding the
// prefix and the dashes.
const inviteBodyLen = inviteGroups * inviteGroupLen

// NewInviteCode returns a fresh code in canonical form, e.g. CF-ABCD-EFGH.
//
// It reads from crypto/rand and does not fall back to anything weaker: a
// math/rand code would be predictable from the mint time, and the failure
// would look exactly like a working code. 8 characters from a 30-symbol
// alphabet is ~39 bits, which is far short of a password and does not need to
// be one -- a guessed code buys a signup slot, not an account, because
// redeeming it still requires a full Spotify OAuth grant from the guesser.
func NewInviteCode() (string, error) {
	buf := make([]byte, inviteBodyLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	body := make([]byte, inviteBodyLen)
	for i, b := range buf {
		// Modulo bias across a 30-symbol alphabet over 256 values is
		// negligible at this entropy level and this is not a key.
		body[i] = inviteAlphabet[int(b)%len(inviteAlphabet)]
	}
	return groupInviteCode(string(body)), nil
}

// groupInviteCode renders a bare body as PREFIX-XXXX-XXXX.
func groupInviteCode(body string) string {
	parts := make([]string, 0, inviteGroups+1)
	parts = append(parts, invitePrefix)
	for g := range inviteGroups {
		parts = append(parts, body[g*inviteGroupLen:(g+1)*inviteGroupLen])
	}
	return strings.Join(parts, "-")
}

// NormalizeInviteCode turns what a person typed into the canonical stored
// form. Codes are minted uppercase and dashed; they come back lowercased, with
// the dashes eaten by a chat client, with spaces where the dashes were, or
// with a phone keyboard having capitalised only the first letter.
//
// It works by discarding every separator to get at the body, then putting the
// canonical dashes back -- NOT by stripping separators and comparing. That
// distinction is the whole bug this function had: stripping alone made
// "CFT9CSBFVA" stop matching the stored "CF-T9CS-BFVA" rather than start
// matching it, so the spelling the doc comment promised to handle was the one
// spelling that failed. Anything that is not a well-formed code falls through
// as its stripped uppercase self, which matches nothing and is refused.
//
// There is exactly one of these for the same reason there is exactly one
// HashSessionToken: a second spelling of the normalizer refuses valid invites,
// and the person holding one is told their code is broken.
func NormalizeInviteCode(s string) string {
	var body strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			body.WriteRune(r)
		}
	}
	stripped := body.String()
	// Re-group only a code shaped like one of ours. A value of any other
	// shape is returned stripped, so it still normalizes deterministically
	// and still fails to match a real row.
	if rest, ok := strings.CutPrefix(stripped, invitePrefix); ok && len(rest) == inviteBodyLen {
		return groupInviteCode(rest)
	}
	return stripped
}

// CreateInviteCode mints a code. maxRedemptions must be at least 1; expiresAt
// nil means it never expires.
func CreateInviteCode(ctx context.Context, pool *pgxpool.Pool, code, note string, maxRedemptions int, expiresAt *time.Time) (InviteCode, error) {
	code = NormalizeInviteCode(code)
	if code == "" {
		return InviteCode{}, errors.New("create invite: code is empty")
	}
	if maxRedemptions < 1 {
		return InviteCode{}, fmt.Errorf("create invite: max redemptions must be at least 1, got %d", maxRedemptions)
	}
	const q = `
INSERT INTO invite_codes (code, note, max_redemptions, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING code, note, max_redemptions, redemptions, expires_at, disabled_at, created_at`
	return scanInvite(pool.QueryRow(ctx, q, code, note, maxRedemptions, expiresAt))
}

// ListInviteCodes returns every code, newest first. The table is an operator's
// notebook, not a hot path — there is no pagination because there is no
// deployment where the count justifies it.
func ListInviteCodes(ctx context.Context, pool *pgxpool.Pool) ([]InviteCode, error) {
	const q = `
SELECT code, note, max_redemptions, redemptions, expires_at, disabled_at, created_at
FROM invite_codes ORDER BY created_at DESC`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()

	var out []InviteCode
	for rows.Next() {
		c, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DisableInviteCode revokes a code without deleting it, so a user admitted by
// it keeps their provenance. Disabling an already-disabled code is a no-op
// rather than an error — the operator's intent is satisfied either way.
func DisableInviteCode(ctx context.Context, pool *pgxpool.Pool, code string) error {
	code = NormalizeInviteCode(code)
	const q = `UPDATE invite_codes SET disabled_at = COALESCE(disabled_at, now()) WHERE code = $1`
	tag, err := pool.Exec(ctx, q, code)
	if err != nil {
		return fmt.Errorf("disable invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoRows
	}
	return nil
}

// InviteCodeUsable is the read-only pre-check run at /login, before the
// browser is sent to Spotify. It deliberately does not reserve or redeem
// anything: a login that is started and abandoned must not spend a code, and
// most abandoned logins are abandoned at Spotify's consent screen, which is
// after this point and before the redemption.
//
// The consequence is that this answer can be stale by the time /callback
// redeems — two people can both pass the pre-check on a one-use code and only
// one can pass the redemption. That is the correct direction to be wrong in:
// the redemption is the authority and it is atomic.
func InviteCodeUsable(ctx context.Context, pool *pgxpool.Pool, code string) (bool, error) {
	code = NormalizeInviteCode(code)
	const q = `SELECT EXISTS (SELECT 1 FROM invite_codes WHERE code = $1 AND ` + inviteUsableSQL + `)`
	var ok bool
	if err := pool.QueryRow(ctx, q, code).Scan(&ok); err != nil {
		return false, fmt.Errorf("check invite: %w", err)
	}
	return ok, nil
}

// UpsertUserWithAdmission is the login callback's one write. It updates the
// user when the Spotify account is already known and creates one when it is
// not, and it is only the second case that has to get past the invite gate.
//
// That asymmetry is the whole admission policy in one place:
//
//   - A returning user never sees the gate. Their row exists, so no code is
//     consulted and none is spent.
//   - Every account that predates migration 0021 is therefore grandfathered
//     without anybody backfilling a column.
//   - Only a genuinely new Spotify account has to present a code.
//
// requireInvite is passed explicitly rather than inferred from an empty
// `invite` string, because "the gate is off" and "the gate is on and the
// caller sent nothing" must produce different outcomes and an empty string
// cannot say which one it means.
//
// Redemption and insertion share one transaction. Split across two statements
// this has two failure modes and both are silent: redeem-then-insert burns a
// code when the insert fails, and insert-then-redeem admits a user for free
// when the redemption fails. Inside a transaction neither is reachable — the
// code is spent if and only if the user exists.
func UpsertUserWithAdmission(ctx context.Context, pool *pgxpool.Pool, u User, invite string, requireInvite bool) (User, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	// Normalized once, here, so the string that gets redeemed and the string
	// recorded in invited_with are the same one.
	invite = NormalizeInviteCode(invite)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Does this Spotify account already have a row? This is the question the
	// gate turns on, so it is asked inside the transaction rather than by the
	// caller beforehand — a lookup outside it could go stale between the
	// answer and the write.
	const existing = `SELECT id FROM users WHERE spotify_user_id = $1 FOR UPDATE`
	var existingID uuid.UUID
	err = tx.QueryRow(ctx, existing, u.SpotifyUserID).Scan(&existingID)
	switch {
	case err == nil:
		// Known account. Keep the id the rest of the database references.
		u.ID = existingID
	case errors.Is(err, pgx.ErrNoRows):
		// New account: this is a signup, and signups are what the gate is for.
		if requireInvite {
			if invite == "" {
				return User{}, ErrInviteRequired
			}
			if err := redeemInviteTx(ctx, tx, invite); err != nil {
				return User{}, err
			}
			u.InvitedWith = invite
		}
	default:
		return User{}, fmt.Errorf("admission lookup: %w", err)
	}

	// Email is COALESCE-preserved so a re-login without the scope doesn't
	// blank out a previously-captured address. invited_with is only ever
	// written on insert: it records who let this account in, and a later login
	// carrying a different code does not change that answer.
	const upsert = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce, email, invited_with)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''))
ON CONFLICT (spotify_user_id) DO UPDATE SET
  display_name            = EXCLUDED.display_name,
  encrypted_refresh_token = EXCLUDED.encrypted_refresh_token,
  refresh_token_nonce     = EXCLUDED.refresh_token_nonce,
  email                   = COALESCE(EXCLUDED.email, users.email),
  updated_at              = now()
RETURNING id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce,
          COALESCE(email, ''), digest_opt_in, instant_notify_opt_in, push_opt_in,
          COALESCE(invited_with, '')`
	row := tx.QueryRow(ctx, upsert, u.ID, u.SpotifyUserID, u.DisplayName,
		u.EncryptedRefreshToken, u.RefreshTokenNonce, u.Email, u.InvitedWith)
	var out User
	if err := row.Scan(&out.ID, &out.SpotifyUserID, &out.DisplayName, &out.EncryptedRefreshToken,
		&out.RefreshTokenNonce, &out.Email, &out.DigestOptIn, &out.InstantNotifyOptIn,
		&out.PushOptIn, &out.InvitedWith); err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit admission: %w", err)
	}
	return out, nil
}

// redeemInviteTx spends one redemption, atomically. The guard lives in the
// WHERE clause rather than in a read followed by a write, so two callers
// racing for the last seat on a code cannot both win: one UPDATE matches, the
// other matches no row.
func redeemInviteTx(ctx context.Context, tx pgx.Tx, code string) error {
	const q = `
UPDATE invite_codes SET redemptions = redemptions + 1
WHERE code = $1 AND ` + inviteUsableSQL
	tag, err := tx.Exec(ctx, q, code)
	if err != nil {
		return fmt.Errorf("redeem invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInviteInvalid
	}
	return nil
}

// rowScanner is satisfied by both pgx.Row and pgx.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanInvite(r rowScanner) (InviteCode, error) {
	var c InviteCode
	if err := r.Scan(&c.Code, &c.Note, &c.MaxRedemptions, &c.Redemptions,
		&c.ExpiresAt, &c.DisabledAt, &c.CreatedAt); err != nil {
		return InviteCode{}, fmt.Errorf("scan invite: %w", err)
	}
	return c, nil
}
