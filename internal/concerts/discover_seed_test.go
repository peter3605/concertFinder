package concerts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// The seed's whole delivery mechanism is that its rows are indistinguishable
// from a scan's. If the key falls outside CachePrefixTicketmaster, the view it
// exists to fill never reads it and the janitor never expires it — and both of
// those are silent.
func TestDiscoverSeedCacheKeyIsInsideTheTicketmasterPrefix(t *testing.T) {
	loc := DiscoverSeedLocation(40.7128, -74.0060)
	key := DiscoverSeedCacheKey(loc)

	if !strings.HasPrefix(key, CachePrefixTicketmaster) {
		t.Fatalf("key %q must start with %q or the discover view will never read it", key, CachePrefixTicketmaster)
	}
	if !strings.Contains(key, DiscoverSeedArtistID) {
		t.Errorf("key %q should name itself a seed so an operator can tell it from an artist's listing", key)
	}
	// Distinct from any artist's row for the same place: a seed that collided
	// with a real artist would overwrite that artist's cached listing with the
	// whole city, and a scan would then be answered from it.
	if artistKey := cacheKey(CachePrefixTicketmaster, "3TVXtAsR1Inumwj472S9r4", loc); key == artistKey {
		t.Errorf("seed key collides with an artist key: %q", key)
	}
}

// Different cities must not share a row. They did not by construction, but the
// key is built from a Location whose radius is fixed, so the coordinate is the
// only thing separating them.
func TestDiscoverSeedCacheKeyIsPerCity(t *testing.T) {
	ny := DiscoverSeedCacheKey(DiscoverSeedLocation(40.7128, -74.0060))
	la := DiscoverSeedCacheKey(DiscoverSeedLocation(34.0522, -118.2437))
	if ny == la {
		t.Fatalf("New York and Los Angeles share a cache key: %q", ny)
	}
}

// End to end through the data path the signed-out view actually uses: a
// seeded payload is an ordinary []ticketmaster.Event, so FromCachedTicketmaster
// must decode it with no special case — and must still refuse to put an
// artist ID on the acts, because a Ticketmaster attraction ID in that field is
// a save pointed at an artist that does not exist.
func TestSeededPayloadDecodesLikeAnyCachedRow(t *testing.T) {
	notBefore := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	blob, err := json.Marshal([]ticketmaster.Event{{
		ID:        "e1",
		Name:      "Two Acts at the Room",
		URL:       "https://example.test/e1",
		Start:     time.Date(2099, 6, 1, 23, 0, 0, 0, time.UTC),
		LocalDate: "2099-06-01",
		Lineup: []ticketmaster.Attraction{
			{ID: "K8vZ917Gku7", Name: "Headliner"},
			{ID: "K8vZ917Gku8", Name: "Support"},
		},
		Venue: ticketmaster.Venue{
			Name: "The Room", City: "New York", State: "NY", Country: "US",
			Latitude: 40.7128, Longitude: -74.0060,
		},
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := FromCachedTicketmaster([][]byte{blob}, notBefore)
	if len(got) != 2 {
		t.Fatalf("concerts = %d, want 2 (one per act on the bill)", len(got))
	}
	for _, c := range got {
		if c.Artist.ID != "" {
			t.Errorf("act %q carries artist ID %q — the IDs in this package are Spotify's, and a TM attraction ID here is a save pointed at nothing",
				c.Artist.Name, c.Artist.ID)
		}
		if c.DedupKey == "" {
			t.Errorf("act %q has no dedup key", c.Artist.Name)
		}
	}

	// And it is reachable from the city it was seeded for, which is the
	// property the whole job is bought for.
	near := Near(got, DiscoverSeedLocation(40.7128, -74.0060))
	if len(near) != 2 {
		t.Errorf("Near returned %d of %d acts for the seeded city", len(near), len(got))
	}
}
