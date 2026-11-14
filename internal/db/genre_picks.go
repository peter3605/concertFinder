package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxGenrePicks is how many genres one account may pick (docs/public-path.md
// §2.2). A genre listing costs up to ticketmaster.MaxGenrePages calls per
// grid cell on a cache miss, so five genres bound a unique-location user at
// fifteen calls a scan.
const MaxGenrePicks = 5

// GenrePick is one top-level Ticketmaster Music genre a user chose.
type GenrePick struct {
	GenreID string `json:"genre_id"`
	Name    string `json:"name"`
}

// AddGenrePick stores a pick. Idempotent: picking a genre already held
// refreshes its name. Returns false when the user already holds
// MaxGenrePicks and this one is not among them. Same single-statement cap as
// AddArtistPick.
func AddGenrePick(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, p GenrePick) (bool, error) {
	const q = `
WITH written AS (
  INSERT INTO user_genre_picks (user_id, genre_id, name)
  SELECT $1::uuid, $2::text, $3::text
  WHERE (SELECT count(*) FROM user_genre_picks WHERE user_id = $1) < $4::int
     OR EXISTS (
       SELECT 1 FROM user_genre_picks WHERE user_id = $1 AND genre_id = $2
     )
  ON CONFLICT (user_id, genre_id) DO UPDATE SET name = EXCLUDED.name
  RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM written)
`
	var ok bool
	if err := pool.QueryRow(ctx, q, userID, p.GenreID, p.Name, MaxGenrePicks).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// RemoveGenrePick deletes a pick. Removing one that is not held is not an
// error.
func RemoveGenrePick(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, genreID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM user_genre_picks WHERE user_id = $1 AND genre_id = $2`, userID, genreID)
	return err
}

// ListGenrePicks returns the user's genre picks in the order they were made.
func ListGenrePicks(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) ([]GenrePick, error) {
	const q = `
SELECT genre_id, name
FROM user_genre_picks
WHERE user_id = $1
ORDER BY created_at, genre_id
`
	rows, err := pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GenrePick{}
	for rows.Next() {
		var p GenrePick
		if err := rows.Scan(&p.GenreID, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
