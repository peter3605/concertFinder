package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminAccount is what `-list-admins` prints: enough to recognise a person,
// and nothing else.
//
// It exists instead of returning []User because User carries the encrypted
// refresh token and its nonce. Those have no business being read out of the
// database to render a list of names -- the rule in CLAUDE.md is that tokens
// are never logged or returned, and the cheapest way to keep it is for the
// query that feeds a listing to never select them in the first place.
type AdminAccount struct {
	ID            uuid.UUID
	SpotifyUserID string
	DisplayName   string
	CreatedAt     time.Time
}

// SetAdmin grants or revokes the admin flag for a Spotify account, returning
// ErrNoRows when no user has signed in with that Spotify ID.
//
// The account is named by spotify_user_id rather than by users.id, and that is
// a usability decision with a correctness edge. The internal UUID is opaque:
// the operator cannot look their own up without a database session, which is
// precisely the thing this is supposed to replace. The Spotify ID is stable,
// unique (there is a unique constraint on the column), and is the identifier a
// person can actually read off their own profile.
//
// Grant and revoke are one function taking a bool rather than two functions,
// because a privilege that can be granted and not removed is a one-way door,
// and the second direction should not be a second query that can drift from
// the first.
func SetAdmin(ctx context.Context, pool *pgxpool.Pool, spotifyUserID string, admin bool) (AdminAccount, error) {
	const q = `
UPDATE users SET is_admin = $2, updated_at = now()
WHERE spotify_user_id = $1
RETURNING id, spotify_user_id, display_name, created_at`
	var a AdminAccount
	err := pool.QueryRow(ctx, q, spotifyUserID, admin).
		Scan(&a.ID, &a.SpotifyUserID, &a.DisplayName, &a.CreatedAt)
	if err != nil {
		// Returned bare rather than wrapped, matching DisableInviteCode: the
		// caller's next line is a comparison against ErrNoRows, and "no such
		// account" is a sentence for the operator, not a stack of context.
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminAccount{}, ErrNoRows
		}
		return AdminAccount{}, fmt.Errorf("set admin: %w", err)
	}
	return a, nil
}

// ListAdmins returns every account carrying the flag, oldest first so the
// bootstrap admin heads the list.
func ListAdmins(ctx context.Context, pool *pgxpool.Pool) ([]AdminAccount, error) {
	const q = `
SELECT id, spotify_user_id, display_name, created_at
FROM users WHERE is_admin ORDER BY created_at ASC`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list admins: %w", err)
	}
	defer rows.Close()

	var out []AdminAccount
	for rows.Next() {
		var a AdminAccount
		if err := rows.Scan(&a.ID, &a.SpotifyUserID, &a.DisplayName, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan admin: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
