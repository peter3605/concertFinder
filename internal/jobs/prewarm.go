package jobs

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// PrewarmSkip says why a login did not pre-warm a scan. Empty means it did.
type PrewarmSkip string

const (
	// PrewarmNoSpotify: the account has no live Spotify connection. The
	// pre-warm exists to have a Spotify profile's feed ready on arrival; an
	// account without one has nothing for it to warm, and its first feed
	// read enqueues whatever scan its picks call for (CF-32).
	PrewarmNoSpotify PrewarmSkip = "no spotify connection"
	// PrewarmNoLocation: the user has not named a place yet. Scanning the
	// deployment default would reserve quota for a city they never chose,
	// and the result would be filed under a location_key nothing reads.
	// Both clients ask for a location before the first feed, and the SWR
	// read enqueues the scan when the answer arrives.
	PrewarmNoLocation PrewarmSkip = "no location yet"
)

// LoginPrewarm decides whether a successful login should enqueue a scan, and
// returns its arguments when it should. It is the whole of the
// OnLoginSuccess hook's judgement, kept here rather than in main.go so the
// skips are testable.
func LoginPrewarm(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (ScanConcertsArgs, PrewarmSkip, error) {
	user, err := db.GetUserByID(ctx, pool, userID)
	if err != nil {
		return ScanConcertsArgs{}, "", fmt.Errorf("prewarm user lookup: %w", err)
	}
	if !user.HasSpotifyCredential() {
		return ScanConcertsArgs{}, PrewarmNoSpotify, nil
	}
	ul, hit, err := db.GetUserLocation(ctx, pool, userID)
	if err != nil {
		return ScanConcertsArgs{}, "", fmt.Errorf("prewarm location lookup: %w", err)
	}
	if !hit {
		return ScanConcertsArgs{}, PrewarmNoLocation, nil
	}
	return ScanConcertsArgs{
		UserID:      userID,
		Latitude:    ul.Latitude,
		Longitude:   ul.Longitude,
		RadiusMiles: ul.RadiusMiles,
	}, "", nil
}
