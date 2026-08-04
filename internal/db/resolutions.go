package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ArtistResolution maps a Spotify artist ID to its identifiers on external
// ticketing platforms. Empty fields mean "we checked and there wasn't one" —
// used as a negative cache so we don't re-hit the API for known-misses.
//
// The artist's official homepage URL used to live here too but was moved
// to mb_url_cache (keyed on name) to eliminate a two-source-of-truth split
// with the MusicBrainz resolver's DB warm layer.
type ArtistResolution struct {
	SpotifyArtistID          string
	TicketmasterAttractionID string
	BandsintownName          string
	// ResolvedAt lets callers expire *negative* resolutions. A row with an
	// empty TicketmasterAttractionID means "TM had no exact name match when
	// we asked", which stops being true when an artist signs to TM — so
	// concerts.needsTMResolution re-asks once the row ages past
	// NegativeResolutionTTL.
	ResolvedAt time.Time
}

// GetArtistResolution returns (row, true, nil) on hit, (zero, false, nil) on miss.
func GetArtistResolution(ctx context.Context, pool *pgxpool.Pool, spotifyArtistID string) (ArtistResolution, bool, error) {
	const q = `SELECT spotify_artist_id,
	                  COALESCE(ticketmaster_attraction_id, ''),
	                  COALESCE(bandsintown_name, ''),
	                  resolved_at
	           FROM artist_resolutions WHERE spotify_artist_id = $1`
	var r ArtistResolution
	err := pool.QueryRow(ctx, q, spotifyArtistID).Scan(&r.SpotifyArtistID, &r.TicketmasterAttractionID, &r.BandsintownName, &r.ResolvedAt)
	if err != nil {
		if errors.Is(err, ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
			return ArtistResolution{}, false, nil
		}
		return ArtistResolution{}, false, err
	}
	return r, true, nil
}

// UpsertArtistResolution stores or refreshes a resolution row. Empty strings
// in the input are treated as "don't touch this field" — not "clear it" —
// so a caller updating only one field doesn't nuke previously-resolved
// values.
func UpsertArtistResolution(ctx context.Context, pool *pgxpool.Pool, r ArtistResolution) error {
	const q = `
INSERT INTO artist_resolutions (spotify_artist_id, ticketmaster_attraction_id, bandsintown_name, resolved_at)
VALUES ($1, NULLIF($2,''), NULLIF($3,''), now())
ON CONFLICT (spotify_artist_id) DO UPDATE SET
  ticketmaster_attraction_id = COALESCE(EXCLUDED.ticketmaster_attraction_id, artist_resolutions.ticketmaster_attraction_id),
  bandsintown_name           = COALESCE(EXCLUDED.bandsintown_name,           artist_resolutions.bandsintown_name),
  resolved_at                = EXCLUDED.resolved_at
`
	_, err := pool.Exec(ctx, q, r.SpotifyArtistID, r.TicketmasterAttractionID, r.BandsintownName)
	return err
}
