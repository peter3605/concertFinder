package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubscribedArtist is one entry in the user's follow list, with the display
// name cached at subscribe time.
type SubscribedArtist struct {
	SpotifyArtistID string
	DisplayName     string
}

// SubscribeArtist adds an artist. Idempotent — a repeat call refreshes the
// display_name in case the artist got renamed. Returns false when the user
// already holds maxSubscribed artists and this one is not among them.
//
// The cap is part of the insert for the same reason as SaveConcert's:
// checking it in the handler leaves a window between the count and the write
// that a client in a loop lives inside. The OR EXISTS in the WHERE is what
// keeps the rename path working at the cap — a full list must still be able
// to correct a name it already holds.
func SubscribeArtist(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, spotifyArtistID, displayName string, maxSubscribed int) (bool, error) {
	const q = `
WITH written AS (
  INSERT INTO user_subscribed_artists (user_id, spotify_artist_id, display_name, subscribed_at)
  -- Casts because the parameters first appear inside an INSERT ... SELECT,
  -- where Postgres resolves their types from the SELECT rather than from the
  -- target columns.
  SELECT $1::uuid, $2::text, NULLIF($3::text, ''), now()
  WHERE (SELECT count(*) FROM user_subscribed_artists WHERE user_id = $1) < $4::int
     OR EXISTS (
       SELECT 1 FROM user_subscribed_artists
        WHERE user_id = $1 AND spotify_artist_id = $2
     )
  ON CONFLICT (user_id, spotify_artist_id) DO UPDATE
    SET display_name = COALESCE(EXCLUDED.display_name, user_subscribed_artists.display_name)
  RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM written)
`
	var ok bool
	if err := pool.QueryRow(ctx, q, userID, spotifyArtistID, displayName, maxSubscribed).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// ListSubscribedArtists returns the full list, ordered by display_name (or
// artist ID when name is missing). Powers the /subscribe page.
func ListSubscribedArtists(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) ([]SubscribedArtist, error) {
	const q = `
SELECT spotify_artist_id, COALESCE(display_name, '')
FROM user_subscribed_artists
WHERE user_id = $1
ORDER BY LOWER(COALESCE(display_name, spotify_artist_id))
`
	rows, err := pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubscribedArtist
	for rows.Next() {
		var a SubscribedArtist
		if err := rows.Scan(&a.SpotifyArtistID, &a.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UnsubscribeArtist removes an artist. Idempotent — missing rows are fine.
func UnsubscribeArtist(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, spotifyArtistID string) error {
	const q = `DELETE FROM user_subscribed_artists WHERE user_id = $1 AND spotify_artist_id = $2`
	_, err := pool.Exec(ctx, q, userID, spotifyArtistID)
	return err
}

// GetSubscribedArtistIDs returns the set of spotify_artist_ids this user
// follows. Used both by the concerts handler (to tag each concert with
// subscribed:bool) and by the scan worker (to decide instant-notify targets).
func GetSubscribedArtistIDs(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (map[string]struct{}, error) {
	const q = `SELECT spotify_artist_id FROM user_subscribed_artists WHERE user_id = $1`
	rows, err := pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// SetInstantNotifyOptIn flips the master switch for instant notifications.
func SetInstantNotifyOptIn(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, optIn bool) error {
	const q = `UPDATE users SET instant_notify_opt_in = $2, updated_at = now() WHERE id = $1`
	tag, err := pool.Exec(ctx, q, userID, optIn)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoRows
	}
	return nil
}
