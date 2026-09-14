package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// End to end through the real write and the real read, because the two agree
// only by string prefix and nothing at compile time checks that. A seed key
// outside concerts.CachePrefixTicketmaster is a row the signed-out landing
// page never sees and the janitor never prunes — with no error, no log, and a
// green build.
//
// This is also the closest a test can get to the story's own acceptance:
// given a city seeded, the query the discover view runs returns it.
func TestSeedDiscoverRowIsVisibleToTheDiscoverPrefixScan(t *testing.T) {
	pool := emailTestPool(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(onePage))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	city := config.SeedCity{Name: "New York", Latitude: 40.7128, Longitude: -74.0060}
	key := concerts.DiscoverSeedCacheKey(concerts.DiscoverSeedLocation(city.Latitude, city.Longitude))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM concert_cache WHERE cache_key = $1`, key)
	})

	// save left nil on purpose: this is the production write path.
	w := &SeedDiscoverWorker{
		Pool: pool,
		TM: ticketmaster.NewClient(&http.Client{
			Timeout:   5 * time.Second,
			Transport: rewriteTransport{base: base, next: http.DefaultTransport},
		}, "test-key"),
		Cities: []config.SeedCity{city},
	}
	if err := w.Work(ctx, &river.Job[SeedDiscoverArgs]{}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	// The query webhttp.DiscoverHandler runs, with its own constants.
	blobs, err := db.ScanCachedConcerts(ctx, pool, concerts.CachePrefixTicketmaster, 7*24*time.Hour, 2000)
	if err != nil {
		t.Fatalf("ScanCachedConcerts: %v", err)
	}

	// And the decode the view does, filtered to the city that was seeded.
	// notBefore is well before the fixture's 2099 date, so anything missing
	// here is missing because it was never written or never matched.
	candidates := concerts.FromCachedTicketmaster(blobs, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	near := concerts.Near(candidates, concerts.DiscoverSeedLocation(city.Latitude, city.Longitude))
	if len(near) == 0 {
		t.Fatalf("seeded %s under %q, but the discover view's own read returned nothing for it", city.Name, key)
	}
	for _, c := range near {
		if c.Artist.ID != "" {
			t.Errorf("seeded act %q carries an artist ID (%q); the IDs in concerts are Spotify's",
				c.Artist.Name, c.Artist.ID)
		}
	}
}
