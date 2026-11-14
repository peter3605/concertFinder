package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxArtistPicks is how many artists one account may pick by hand
// (docs/public-path.md §2.2). Each pick costs about one Ticketmaster call per
// scan, so this is the line that keeps a manual user's nightly spend at the
// size the account-wide budget was planned around.
const MaxArtistPicks = 30

// ArtistPick is one artist a user chose from Ticketmaster's attraction search.
type ArtistPick struct {
	AttractionID string `json:"attraction_id"`
	Name         string `json:"name"`
	// Genre is Ticketmaster's, empty when the attraction has none.
	Genre string `json:"genre,omitempty"`
}

// AddArtistPick stores a pick. Idempotent: picking an artist already held
// refreshes its name and genre. Returns false when the user already holds
// MaxArtistPicks and this one is not among them.
//
// The cap check and the insert are one statement for the same reason as
// SubscribeArtist's, and the OR EXISTS keeps a full list able to refresh an
// entry it already holds.
func AddArtistPick(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, p ArtistPick) (bool, error) {
	const q = `
WITH written AS (
  INSERT INTO user_artist_picks (user_id, attraction_id, name, genre)
  SELECT $1::uuid, $2::text, $3::text, NULLIF($4::text, '')
  WHERE (SELECT count(*) FROM user_artist_picks WHERE user_id = $1) < $5::int
     OR EXISTS (
       SELECT 1 FROM user_artist_picks WHERE user_id = $1 AND attraction_id = $2
     )
  ON CONFLICT (user_id, attraction_id) DO UPDATE
    SET name = EXCLUDED.name, genre = EXCLUDED.genre
  RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM written)
`
	var ok bool
	if err := pool.QueryRow(ctx, q, userID, p.AttractionID, p.Name, p.Genre, MaxArtistPicks).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// RemoveArtistPick deletes a pick. Removing one that is not held is not an
// error.
func RemoveArtistPick(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, attractionID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM user_artist_picks WHERE user_id = $1 AND attraction_id = $2`, userID, attractionID)
	return err
}

// ListArtistPicks returns the user's picks in the order they were made, which
// is the order the scan searches them in.
func ListArtistPicks(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) ([]ArtistPick, error) {
	const q = `
SELECT attraction_id, name, COALESCE(genre, '')
FROM user_artist_picks
WHERE user_id = $1
ORDER BY created_at, attraction_id
`
	rows, err := pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtistPick{}
	for rows.Next() {
		var p ArtistPick
		if err := rows.Scan(&p.AttractionID, &p.Name, &p.Genre); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HasArtistPicks reports whether the user holds at least one pick. It is the
// question the feed asks on every poll, so it is an EXISTS, not a count.
func HasArtistPicks(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_artist_picks WHERE user_id = $1)`, userID).Scan(&ok)
	return ok, err
}
