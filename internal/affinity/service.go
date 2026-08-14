// Package affinity orchestrates the load-or-compute affinity path: check the
// 24h DB cache, and on miss, refresh a Spotify access token, hydrate all
// affinity sources, score, and persist. Exposed as a Service so both the HTTP
// handler and the river-based background refresh use identical logic.
package affinity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/spotify"
)

const ComputeTimeout = 60 * time.Second

type Service struct {
	Pool    *pgxpool.Pool
	Tokens  *auth.TokenService
	Spotify *spotify.Client
	TTL     time.Duration // §7.3 = 24h
}

// User is what LoadOrCompute needs; a tiny subset of auth.CurrentUser so
// callers outside the request context (background jobs) can supply it.
type User struct {
	ID            uuid.UUID
	SpotifyUserID string
}

// LoadOrCompute returns the caller's affinity profile, computing on cache miss.
func (s *Service) LoadOrCompute(ctx context.Context, u User) (artists []spotify.ScoredArtist, computedAt time.Time, cached bool, err error) {
	if blob, ts, ok, err := db.LoadFreshAffinityProfile(ctx, s.Pool, u.ID, s.TTL); err != nil {
		return nil, time.Time{}, false, fmt.Errorf("load affinity: %w", err)
	} else if ok {
		if err := json.Unmarshal(blob, &artists); err != nil {
			return nil, time.Time{}, false, fmt.Errorf("decode affinity: %w", err)
		}
		return artists, ts, true, nil
	}
	artists, err = s.Compute(ctx, u)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return artists, time.Now(), false, nil
}

// Compute forces a fresh computation and persists the result.
func (s *Service) Compute(ctx context.Context, u User) ([]spotify.ScoredArtist, error) {
	cctx, cancel := context.WithTimeout(ctx, ComputeTimeout)
	defer cancel()
	accessToken, err := s.Tokens.AccessTokenFor(cctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("access token: %w", err)
	}
	// Partial hydration is usable: HydrateSources only reports an error set
	// with no data at all when *every* source failed. A profile built from
	// five of six signals is worth persisting — the alternative is no feed.
	sources, hydrateErrs := s.Spotify.HydrateSources(cctx, accessToken, u.SpotifyUserID)
	if len(hydrateErrs) > 0 {
		slog.Warn("affinity: some sources failed to hydrate",
			"user", u.ID, "failed", len(hydrateErrs), "errs", errors.Join(hydrateErrs...))
	}
	artists := spotify.ScoreArtists(sources)
	if len(artists) == 0 {
		// No signal at all. Persisting this would cache an empty profile for
		// the full 24h TTL and hand the scan worker nothing to search.
		return nil, fmt.Errorf("hydrate: no affinity signal: %w", errors.Join(hydrateErrs...))
	}
	blob, err := json.Marshal(artists)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	if err := db.SaveAffinityProfile(ctx, s.Pool, u.ID, blob); err != nil {
		return artists, fmt.Errorf("save: %w", err)
	}
	return artists, nil
}
