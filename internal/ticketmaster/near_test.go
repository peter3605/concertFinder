package ticketmaster

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// SearchEventsNear is the city-wide query behind the signed-out discover
// view's seed. What matters about it is mostly what it does NOT send: an
// attractionId would make it one artist's dates again, which is exactly the
// listing the seed cannot use.
func TestSearchEventsNearOmitsAttraction(t *testing.T) {
	c, rec := newTestAPI(t, serveFixture(t, "events_venue.json"))

	if _, _, err := c.SearchEventsNear(context.Background(), testLat, testLng, testRadius, nil); err != nil {
		t.Fatalf("SearchEventsNear: %v", err)
	}

	reqs := rec.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	q := reqs[0].Query()
	if got := q.Get("attractionId"); got != "" {
		t.Errorf("attractionId = %q, want it absent — a city seed filtered to one act is not a city", got)
	}
	if reqs[0].Path != "/discovery/v2/events.json" {
		t.Errorf("path = %q, want /discovery/v2/events.json", reqs[0].Path)
	}
}

// The seeded population must match what a scan produces, or the signed-out
// view shows a different kind of thing than the app does. Both queries are
// composed by eventsQuery for that reason; this pins the parameters that
// decide it.
func TestSearchEventsNearSharesQueryShapeWithSearchEvents(t *testing.T) {
	c, rec := newTestAPI(t, serveFixture(t, "events_venue.json"))

	if _, _, err := c.SearchEventsNear(context.Background(), testLat, testLng, testRadius, nil); err != nil {
		t.Fatalf("SearchEventsNear: %v", err)
	}
	if _, _, err := c.SearchEvents(context.Background(), "K8vZ917Gku7", testLat, testLng, testRadius, nil); err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}

	reqs := rec.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	near, artist := reqs[0].Query(), reqs[1].Query()
	for _, key := range []string{"latlong", "radius", "unit", "classificationName", "size", "countryCode"} {
		if near.Get(key) != artist.Get(key) {
			t.Errorf("%s: city query has %q, artist query has %q — the two must describe the same population",
				key, near.Get(key), artist.Get(key))
		}
	}
	assertQuery(t, near, "classificationName", "Music")
	assertQuery(t, near, "countryCode", "US")
	assertQuery(t, near, "unit", "miles")
}

// Pagination and the permit contract are shared with SearchEvents, but the
// seed is the caller that pages hardest — a busy market really does run past
// one page — so the behaviour is pinned here too rather than assumed.
func TestSearchEventsNearPaging(t *testing.T) {
	tests := []struct {
		name         string
		totalPages   int
		permit       func() bool
		wantRequests int
		wantComplete bool
	}{
		{
			name:         "single page is complete",
			totalPages:   1,
			permit:       func() bool { return true },
			wantRequests: 1,
			wantComplete: true,
		},
		{
			name:         "follows pages while permitted",
			totalPages:   3,
			permit:       func() bool { return true },
			wantRequests: 3,
			wantComplete: true,
		},
		{
			// A refused permit is the account ledger saying the seed has
			// spent its block. Stopping short is correct; reporting the
			// result complete would let the caller cache a truncated city.
			name:         "refused permit stops and reports incomplete",
			totalPages:   3,
			permit:       func() bool { return false },
			wantRequests: 1,
			wantComplete: false,
		},
		{
			// nil means "first page only" everywhere in this package.
			name:         "nil permit fetches one page",
			totalPages:   3,
			permit:       nil,
			wantRequests: 1,
			wantComplete: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				page := r.URL.Query().Get("page")
				if page == "" {
					page = "0"
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"_embedded":{"events":[{"id":"e%s","name":"Show %s","dates":{"start":{"localDate":"2099-06-01"}},"_embedded":{"venues":[{"name":"Room","city":{"name":"DC"},"location":{"latitude":"38.9","longitude":"-77.0"}}]}}]},"page":{"size":100,"totalPages":%d,"number":%s}}`,
					page, page, tc.totalPages, page)
			})

			var permit PagePermit
			if tc.permit != nil {
				permit = tc.permit
			}
			evs, complete, err := c.SearchEventsNear(context.Background(), testLat, testLng, testRadius, permit)
			if err != nil {
				t.Fatalf("SearchEventsNear: %v", err)
			}
			if rec.count() != tc.wantRequests {
				t.Errorf("requests = %d, want %d", rec.count(), tc.wantRequests)
			}
			if complete != tc.wantComplete {
				t.Errorf("complete = %v, want %v", complete, tc.wantComplete)
			}
			if len(evs) != tc.wantRequests {
				t.Errorf("events = %d, want %d (one per page fetched)", len(evs), tc.wantRequests)
			}
		})
	}
}
