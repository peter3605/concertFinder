package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/jobs"
	"github.com/peterho/concertfinder/internal/spotify"
	"github.com/peterho/concertfinder/internal/testdb"
)

// CF-32: every Spotify-only path, driven for accounts that must not reach
// Spotify. internal/auth/spotify_gate_test.go proves structurally that every
// Spotify call needs a token from TokenService; this proves the paths a
// non-Spotify account can actually trigger refuse before that, and that none
// of them puts a request on the wire. The picks-only scan itself is covered
// by TestPicksOnlyUserScansWithoutSpotify.

// spotifyWire records every request a Spotify client or the token service
// sends, and answers none of them.
type spotifyWire struct {
	mu   sync.Mutex
	seen []string
}

func (s *spotifyWire) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.seen = append(s.seen, r.URL.String())
	s.mu.Unlock()
	return nil, errors.New("spotify is unreachable in this test")
}

// insertSpotifyUser creates an account with a Spotify identity, a live
// session, and either a working-looking credential or a zeroed one (what
// db.DisconnectSpotify leaves).
func insertSpotifyUser(t *testing.T, pool *pgxpool.Pool, connected bool) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	ct, nonce := []byte{}, []byte{}
	if connected {
		ct, nonce = []byte("ciphertext"), []byte("nonce-123456")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, 'CF-32 spotify', $3, $4)`, id, "cf32-"+id.String(), ct, nonce); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	token := uuid.NewString() + uuid.NewString()
	if err := db.CreateSession(ctx, pool, db.Session{
		ID: uuid.NewString(), TokenHash: auth.HashSessionToken(token), UserID: id, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id, token
}

func TestNonSpotifyUsersReachNoSpotifyCall(t *testing.T) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()
	loc := concerts.Location{Latitude: 38.9172, Longitude: -77.0369, RadiusMiles: 50}

	picksID, picksToken := newPicksUser(t, pool, loc)
	goneID, goneToken := insertSpotifyUser(t, pool, false)
	liveID, _ := insertSpotifyUser(t, pool, true)

	wire := &spotifyWire{}
	tokens := &auth.TokenService{Pool: pool, EncKey: make([]byte, 32), ClientID: "test", HTTPClient: &http.Client{Transport: wire}}
	spot := spotify.NewClient(&http.Client{Transport: wire})
	aff := &affinity.Service{Pool: pool, Tokens: tokens, Spotify: spot, TTL: time.Hour}

	// GET /me/artists/search: 403, for both a never-connected and a
	// disconnected account, and with or without a query.
	h := &SubscribedArtistsHandler{Pool: pool, Spotify: spot, Tokens: tokens}
	r := chi.NewRouter()
	r.With(auth.RequireUser(pool)).Get("/me/artists/search", h.SearchArtists)
	for name, tok := range map[string]string{"picks-only": picksToken, "disconnected": goneToken} {
		for _, q := range []string{"?q=radiohead", ""} {
			req := httptest.NewRequest(http.MethodGet, "/me/artists/search"+q, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s search%s = %d %q, want 403", name, q, w.Code, w.Body.String())
			}
		}
	}

	// The nightly affinity fanout enqueues only the live Spotify account.
	// The live one is the positive control: without it a query that matched
	// nobody would pass.
	rc, err := river.NewClient[pgx.Tx](riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	fan := &jobs.FanoutAffinityRefreshWorker{Pool: pool, Client: rc}
	if err := fan.Work(ctx, &river.Job[jobs.FanoutAffinityRefreshArgs]{JobRow: &rivertype.JobRow{}}); err != nil {
		t.Fatalf("fanout: %v", err)
	}
	enqueued := func(id uuid.UUID) int {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM river_job WHERE kind = 'refresh_affinity' AND args->>'user_id' = $1`,
			id.String()).Scan(&n); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		return n
	}
	if n := enqueued(liveID); n != 1 {
		t.Errorf("fanout enqueued %d refreshes for the live Spotify user, want 1", n)
	}
	for name, id := range map[string]uuid.UUID{"picks-only": picksID, "disconnected": goneID} {
		if n := enqueued(id); n != 0 {
			t.Errorf("fanout enqueued %d refreshes for the %s user, want 0", n, name)
		}
	}

	// A refresh that reaches the worker anyway (enqueued before a
	// disconnect) is a quiet success, not a failure river retries.
	ref := &jobs.RefreshAffinityWorker{Pool: pool, Affinity: aff}
	for name, id := range map[string]uuid.UUID{"picks-only": picksID, "disconnected": goneID} {
		job := &river.Job[jobs.RefreshAffinityArgs]{JobRow: &rivertype.JobRow{}, Args: jobs.RefreshAffinityArgs{UserID: id}}
		if err := ref.Work(ctx, job); err != nil {
			t.Errorf("refresh affinity for %s user = %v, want nil", name, err)
		}
		// And the service itself, which the scan worker reaches.
		if _, err := aff.Compute(ctx, affinity.User{ID: id}); !errors.Is(err, auth.ErrSpotifyDisconnected) {
			t.Errorf("affinity.Compute for %s user = %v, want ErrSpotifyDisconnected", name, err)
		}
	}

	// Login pre-warm: skipped for an account with no Spotify connection,
	// even one that has a location and picks.
	if _, skip, err := jobs.LoginPrewarm(ctx, pool, picksID); err != nil || skip != jobs.PrewarmNoSpotify {
		t.Errorf("LoginPrewarm(picks-only) = %q, %v; want %q", skip, err, jobs.PrewarmNoSpotify)
	}
	if _, skip, err := jobs.LoginPrewarm(ctx, pool, goneID); err != nil || skip != jobs.PrewarmNoSpotify {
		t.Errorf("LoginPrewarm(disconnected) = %q, %v; want %q", skip, err, jobs.PrewarmNoSpotify)
	}
	// The live user has no location, so it is skipped for that reason
	// instead -- which shows the Spotify check is not swallowing everyone.
	if _, skip, err := jobs.LoginPrewarm(ctx, pool, liveID); err != nil || skip != jobs.PrewarmNoLocation {
		t.Errorf("LoginPrewarm(live, no location) = %q, %v; want %q", skip, err, jobs.PrewarmNoLocation)
	}
	if err := db.UpsertUserLocation(ctx, pool, db.UserLocation{
		UserID: liveID, Latitude: loc.Latitude, Longitude: loc.Longitude, RadiusMiles: loc.RadiusMiles,
	}); err != nil {
		t.Fatalf("set location: %v", err)
	}
	if args, skip, err := jobs.LoginPrewarm(ctx, pool, liveID); err != nil || skip != "" || args.UserID != liveID {
		t.Errorf("LoginPrewarm(live) = %+v, %q, %v; want a scan", args, skip, err)
	}

	if len(wire.seen) != 0 {
		t.Fatalf("Spotify was contacted on behalf of a non-Spotify account: %v", wire.seen)
	}
}
