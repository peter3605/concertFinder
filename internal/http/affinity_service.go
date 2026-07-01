package http

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/spotify"
)

// AffinityService encapsulates the "load-or-compute the top-N artist list"
// path used by /me/affinity and /me/concerts.
type AffinityService struct {
	Pool    *pgxpool.Pool
	Tokens  *auth.TokenService
	Spotify *spotify.Client
	TTL     time.Duration // §7.3 = 24h
}

const affinityComputeTimeout = 60 * time.Second

// LoadOrCompute returns the caller's affinity profile, computing on cache miss.
// `cached` is true when the fresh copy came from the DB.
func (s *AffinityService) LoadOrCompute(ctx context.Context, user auth.CurrentUser) ([]spotify.ScoredArtist, time.Time, bool, error) {
	if blob, computedAt, ok, err := db.LoadFreshAffinityProfile(ctx, s.Pool, user.ID, s.TTL); err != nil {
		return nil, time.Time{}, false, fmt.Errorf("load affinity: %w", err)
	} else if ok {
		var artists []spotify.ScoredArtist
		if err := json.Unmarshal(blob, &artists); err != nil {
			return nil, time.Time{}, false, fmt.Errorf("decode affinity: %w", err)
		}
		return artists, computedAt, true, nil
	}

	cctx, cancel := context.WithTimeout(ctx, affinityComputeTimeout)
	defer cancel()

	accessToken, err := s.Tokens.AccessTokenFor(cctx, user.ID)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("access token: %w", err)
	}
	sources, err := s.Spotify.HydrateSources(cctx, accessToken, user.SpotifyUserID)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("hydrate: %w", err)
	}
	artists := spotify.ScoreArtists(sources)

	if blob, err := json.Marshal(artists); err == nil {
		if err := db.SaveAffinityProfile(ctx, s.Pool, user.ID, blob); err != nil {
			// Non-fatal; caller may log.
			_ = err
		}
	}
	return artists, time.Now(), false, nil
}
