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

	if _, _, _, err := c.SearchEventsNear(context.Background(), testLat, testLng, testRadius, nil); err != nil {
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

	if _, _, _, err := c.SearchEventsNear(context.Background(), testLat, testLng, testRadius, nil); err != nil {
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

// The city-wide listing is a date-ascending prefix capped at MaxNearPages
// (CF-B23). A cap stop is cacheable with a horizon; a short stop -- a refused
// permit, or nil, which means first page only -- is a fragment and is not.
func TestSearchEventsNearPaging(t *testing.T) {
	tests := []struct {
		name          string
		totalPages    int
		permit        func() bool
		wantRequests  int
		wantCacheable bool
		wantHorizon   bool
	}{
		{name: "single page is whole", totalPages: 1, permit: func() bool { return true }, wantRequests: 1, wantCacheable: true},
		{name: "follows pages while permitted", totalPages: 3, permit: func() bool { return true }, wantRequests: 3, wantCacheable: true},
		{
			// New York at radius 50: 37 pages, more than MaxEventPages.
			name: "dense market stops at the cap, cacheable", totalPages: 37, permit: func() bool { return true },
			wantRequests: MaxNearPages, wantCacheable: true, wantHorizon: true,
		},
		{name: "refused permit is a fragment", totalPages: 3, permit: func() bool { return false }, wantRequests: 1, wantCacheable: false, wantHorizon: true},
		{name: "nil permit fetches one page, a fragment", totalPages: 3, permit: nil, wantRequests: 1, wantCacheable: false, wantHorizon: true},
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
			evs, horizon, cacheable, err := c.SearchEventsNear(context.Background(), testLat, testLng, testRadius, permit)
			if err != nil {
				t.Fatalf("SearchEventsNear: %v", err)
			}
			if rec.count() != tc.wantRequests {
				t.Errorf("requests = %d, want %d", rec.count(), tc.wantRequests)
			}
			if cacheable != tc.wantCacheable {
				t.Errorf("cacheable = %v, want %v", cacheable, tc.wantCacheable)
			}
			if (horizon != "") != tc.wantHorizon {
				t.Errorf("horizon = %q, want set=%v", horizon, tc.wantHorizon)
			}
			if len(evs) != tc.wantRequests {
				t.Errorf("events = %d, want %d (one per page fetched)", len(evs), tc.wantRequests)
			}
			for i, req := range rec.requests() {
				if got := req.Query().Get("sort"); got != "date,asc" {
					t.Errorf("request %d sort = %q, want date,asc", i, got)
				}
			}
		})
	}
}
