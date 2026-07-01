package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ArtistResolution maps a Spotify artist ID to its identifiers on external
// ticketing platforms. Empty fields mean "we checked and there wasn't one" —
// used as a negative cache so we don't re-hit the API for known-misses.
type ArtistResolution struct {
	SpotifyArtistID          string
	TicketmasterAttractionID string
	BandsintownName          string
	OfficialURL              string
}

// GetArtistResolution returns (row, true, nil) on hit, (zero, false, nil) on miss.
func GetArtistResolution(ctx context.Context, pool *pgxpool.Pool, spotifyArtistID string) (ArtistResolution, bool, error) {
	const q = `SELECT spotify_artist_id,
	                  COALESCE(ticketmaster_attraction_id, ''),
	                  COALESCE(bandsintown_name, ''),
	                  COALESCE(official_url, '')
	           FROM artist_resolutions WHERE spotify_artist_id = $1`
	var r ArtistResolution
	err := pool.QueryRow(ctx, q, spotifyArtistID).Scan(&r.SpotifyArtistID, &r.TicketmasterAttractionID, &r.BandsintownName, &r.OfficialURL)
	if err != nil {
		if errors.Is(err, ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
			return ArtistResolution{}, false, nil
		}
		return ArtistResolution{}, false, err
	}
	return r, true, nil
}

// UpsertArtistResolution stores or refreshes a resolution row. Empty strings
// are persisted as NULL so the negative-cache semantics stay explicit.
func UpsertArtistResolution(ctx context.Context, pool *pgxpool.Pool, r ArtistResolution) error {
	const q = `
INSERT INTO artist_resolutions (spotify_artist_id, ticketmaster_attraction_id, bandsintown_name, official_url, resolved_at)
VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), now())
ON CONFLICT (spotify_artist_id) DO UPDATE SET
  ticketmaster_attraction_id = EXCLUDED.ticketmaster_attraction_id,
  bandsintown_name           = EXCLUDED.bandsintown_name,
  official_url               = EXCLUDED.official_url,
  resolved_at                = EXCLUDED.resolved_at
`
	_, err := pool.Exec(ctx, q, r.SpotifyArtistID, r.TicketmasterAttractionID, r.BandsintownName, r.OfficialURL)
	return err
}
