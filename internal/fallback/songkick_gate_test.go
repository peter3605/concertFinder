package fallback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/testdb"
)

// toServer sends every request to srv, whatever host it named, so the
// client's real songkickBase URLs are what the test exercises.
type toServer struct{ srv *url.URL }

func (t toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host = t.srv.Scheme, t.srv.Host
	return http.DefaultTransport.RoundTrip(r)
}

// countingResolver stands in for Tier B and records that each artist got
// there. It resolves nothing, so tryOfficialSite is never reached.
type countingResolver struct{ calls atomic.Int64 }

func (r *countingResolver) ResolveOfficialURL(context.Context, string) (string, error) {
	r.calls.Add(1)
	return "", nil
}

// songkickChain builds a chain whose Songkick tier talks to a server
// answering every request with status, and returns how many requests it got.
func songkickChain(t *testing.T, status int, body string) (*Chain, *countingResolver, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	res := &countingResolver{}
	return &Chain{
		Resolver: res,
		Songkick: &SongkickClient{
			HTTP:   &http.Client{Timeout: 2 * time.Second, Transport: toServer{u}},
			APIKey: songkickSecret,
		},
	}, res, &requests
}

// findAll runs the chain for n artists at once, the way a scan does, all
// sharing one scan's state.
func findAll(ctx context.Context, c *Chain, n int) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.FindEvents(ctx, concerts.Target{Name: "Artist " + string(rune('A'+i))}, concerts.Location{})
		}(i)
	}
	wg.Wait()
}

// A dead key costs one request per scan, not one per artist, and spends no
// permit after that one; every artist still reaches Tier B (CF-B34).
func TestSongkick401StopsSongkickForTheScan(t *testing.T) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()
	uid := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, 'sk-gate', '', 'ct'::bytea, 'n'::bytea)`, uid); err != nil {
		t.Fatal(err)
	}
	ledger := &rate.Ledger{Pool: pool, Caps: rate.Caps{Songkick: 100}}
	rs := ledger.ReserveAll(ctx, uid, 100)
	defer rs.Release(ctx)

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		chain, tierB, requests := songkickChain(t, status, `{}`)
		state := &concerts.FallbackScanState{}
		before := songkickUsed(rs)
		scanCtx := concerts.WithFallbackScanState(rate.NewContext(ctx, rs), state)

		const artists = 8
		findAll(scanCtx, chain, artists)

		if got := requests.Load(); got != 1 {
			t.Errorf("%d: Songkick got %d requests across %d artists, want 1", status, got, artists)
		}
		if got := tierB.calls.Load(); got != artists {
			t.Errorf("%d: Tier B reached by %d artists, want all %d", status, got, artists)
		}
		if got := songkickUsed(rs) - before; got != SongkickCallsPerLookup {
			t.Errorf("%d: spent %d Songkick permits, want %d (the probe only)", status, got, SongkickCallsPerLookup)
		}
		if !state.Songkick.Dead() {
			t.Errorf("%d: scan state does not record Songkick as unauthorized", status)
		}
		if rs.Exhausted(rate.SourceSongkick) || rs.PrimaryExhausted() {
			t.Errorf("%d: a dead Songkick key must not read as a refused permit or an incomplete scan", status)
		}
	}
}

func songkickUsed(rs *rate.Reservations) int64 {
	for _, s := range rs.Stats() {
		if s.Source == rate.SourceSongkick {
			return s.Used
		}
	}
	return 0
}

// A working key is not gated: the probe opens the gate and every artist
// gets its own lookup (here an artist search that matches nobody).
func TestSongkickWorkingKeyIsNotGated(t *testing.T) {
	chain, tierB, requests := songkickChain(t, http.StatusOK, `{"resultsPage":{"results":{}}}`)
	state := &concerts.FallbackScanState{}
	const artists = 8
	findAll(concerts.WithFallbackScanState(context.Background(), state), chain, artists)
	if got := requests.Load(); got != artists {
		t.Fatalf("Songkick got %d requests, want one per artist (%d)", got, artists)
	}
	if got := tierB.calls.Load(); got != artists {
		t.Fatalf("Tier B reached by %d artists, want %d", got, artists)
	}
	if state.Songkick.Dead() {
		t.Fatal("a working key was recorded as unauthorized")
	}
}

// State is per scan: a second scan asks Songkick again rather than
// inheriting the first one's verdict.
func TestSongkickGateDoesNotOutliveTheScan(t *testing.T) {
	chain, _, requests := songkickChain(t, http.StatusUnauthorized, `{}`)
	for scan := 0; scan < 2; scan++ {
		findAll(concerts.WithFallbackScanState(context.Background(), &concerts.FallbackScanState{}), chain, 4)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("two scans made %d Songkick requests, want one each", got)
	}
}
