package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// The discover view is the one concert endpoint a stranger can call, so what
// these tests pin is mostly what it refuses to do: reach past its cache,
// answer for a coordinate it was not given, or grow without bound.

var discoverNow = time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)

// dcVenue and nycVenue are ~200 miles apart, which is outside every radius
// used below and inside none of them.
func dcEvent(name string, start time.Time) ticketmaster.Event {
	return ticketmaster.Event{
		ID:     "tm-" + name,
		Name:   name,
		URL:    "https://www.ticketmaster.com/event/" + name,
		Lineup: []ticketmaster.Attraction{{ID: "K1", Name: name}},
		Start:  start,
		Venue: ticketmaster.Venue{
			Name: "9:30 Club", City: "Washington", State: "DC", Country: "US",
			Latitude: 38.9180, Longitude: -77.0243,
		},
	}
}

func nycEvent(name string, start time.Time) ticketmaster.Event {
	e := dcEvent(name, start)
	e.Venue = ticketmaster.Venue{
		Name: "Bowery Ballroom", City: "New York", State: "NY", Country: "US",
		Latitude: 40.7205, Longitude: -73.9934,
	}
	return e
}

func blobOf(t *testing.T, evs ...ticketmaster.Event) []byte {
	t.Helper()
	b, err := json.Marshal(evs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newDiscover builds a handler backed by the given payloads instead of a
// database, and reports how many times the source was consulted.
func newDiscover(blobs [][]byte) (*DiscoverHandler, *int) {
	calls := 0
	h := &DiscoverHandler{
		now: func() time.Time { return discoverNow },
	}
	h.blobs = func(context.Context) ([][]byte, error) {
		calls++
		return blobs, nil
	}
	return h, &calls
}

func getDiscover(t *testing.T, h *DiscoverHandler, query string) (*httptest.ResponseRecorder, discoverResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/api/discover?"+query, nil))
	var body discoverResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, body
}

func TestDiscoverReturnsOnlyNearbyFutureShows(t *testing.T) {
	yesterday := discoverNow.Add(-30 * time.Hour)
	tomorrow := discoverNow.Add(30 * time.Hour)
	h, _ := newDiscover([][]byte{blobOf(t,
		dcEvent("Nearby", tomorrow),
		dcEvent("AlreadyHappened", yesterday),
		nycEvent("TooFar", tomorrow),
	)})

	_, body := getDiscover(t, h, "lat=38.8951&lng=-77.0364&radius=50")
	if body.Count != 1 || len(body.Events) != 1 {
		t.Fatalf("want exactly the one nearby future show, got %d: %+v", body.Count, body.Events)
	}
	if got := body.Events[0].Acts[0].Artist.Name; got != "Nearby" {
		t.Errorf("wrong event survived: %q", got)
	}
}

// A show a hundred miles away is a real answer for a wide enough radius. The
// same fixture, a wider radius, both events — this is what proves the filter
// is the radius and not something incidental about the second venue.
func TestDiscoverHonoursRadius(t *testing.T) {
	tomorrow := discoverNow.Add(30 * time.Hour)
	h, _ := newDiscover([][]byte{blobOf(t, dcEvent("Near", tomorrow), nycEvent("Far", tomorrow))})

	if _, body := getDiscover(t, h, "lat=38.8951&lng=-77.0364&radius=50"); body.Count != 1 {
		t.Fatalf("50mi: want 1 event, got %d", body.Count)
	}
	if _, body := getDiscover(t, h, "lat=38.8951&lng=-77.0364&radius=300"); body.Count != 2 {
		t.Fatalf("300mi: want 2 events, got %d", body.Count)
	}
}

// The candidate set is decoded once and served to every coordinate. A cache
// keyed on nothing at all would hand the second caller the first caller's
// city; one keyed per request would decode thousands of payloads per hit.
func TestDiscoverReusesOneDecodeAcrossDifferentPlaces(t *testing.T) {
	tomorrow := discoverNow.Add(30 * time.Hour)
	h, calls := newDiscover([][]byte{blobOf(t, dcEvent("Near", tomorrow), nycEvent("Far", tomorrow))})

	_, dc := getDiscover(t, h, "lat=38.8951&lng=-77.0364&radius=50")
	_, nyc := getDiscover(t, h, "lat=40.7128&lng=-74.0060&radius=50")
	if *calls != 1 {
		t.Errorf("want one source read across both requests, got %d", *calls)
	}
	if len(dc.Events) != 1 || dc.Events[0].City != "Washington" {
		t.Errorf("DC request got %+v", dc.Events)
	}
	if len(nyc.Events) != 1 || nyc.Events[0].City != "New York" {
		t.Errorf("NYC request got %+v", nyc.Events)
	}
}

func TestDiscoverRejectsUnusableCoordinates(t *testing.T) {
	h, _ := newDiscover(nil)
	for _, q := range []string{
		"",
		"lat=38.8951",
		"lng=-77.0364",
		"lat=abc&lng=-77.0364",
		"lat=NaN&lng=-77.0364",
		"lat=91&lng=-77.0364",
		"lat=38.8951&lng=181",
	} {
		if rec, _ := getDiscover(t, h, q); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: want 400, got %d", q, rec.Code)
		}
	}
}

// An empty cache is a normal state — nobody has scanned that area, or nobody
// has scanned at all yet — and the clients render nothing for it. A 500 or a
// null events array would both turn that into an error on the first screen a
// stranger sees.
func TestDiscoverEmptyCacheIsAnEmptyList(t *testing.T) {
	h, _ := newDiscover(nil)
	rec, body := getDiscover(t, h, "lat=38.8951&lng=-77.0364")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if body.Count != 0 {
		t.Errorf("want no events, got %d", body.Count)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["events"]) != "[]" {
		t.Errorf("events must serialize as [], got %s", raw["events"])
	}
}

// A database failure with nothing cached yet is the same non-event: fewer
// cards, not an error page.
func TestDiscoverSourceFailureServesNothingRatherThanFailing(t *testing.T) {
	h := &DiscoverHandler{now: func() time.Time { return discoverNow }}
	h.blobs = func(context.Context) ([][]byte, error) { return nil, fmt.Errorf("connection refused") }
	rec, body := getDiscover(t, h, "lat=38.8951&lng=-77.0364")
	if rec.Code != http.StatusOK || body.Count != 0 {
		t.Fatalf("want an empty 200, got %d with %d events", rec.Code, body.Count)
	}
}

// A database outage must not become the slowest thing on the login page: one
// failure holds off the next attempt, so a burst of visitors costs one
// timeout between them rather than one each, serialised.
func TestDiscoverBacksOffAfterAFailure(t *testing.T) {
	now := discoverNow
	calls := 0
	h := &DiscoverHandler{now: func() time.Time { return now }}
	h.blobs = func(context.Context) ([][]byte, error) {
		calls++
		return nil, fmt.Errorf("connection refused")
	}
	for range 5 {
		getDiscover(t, h, "lat=38.8951&lng=-77.0364")
	}
	if calls != 1 {
		t.Fatalf("want one attempt across five requests, got %d", calls)
	}
	now = now.Add(DiscoverFailureBackoff + time.Second)
	getDiscover(t, h, "lat=38.8951&lng=-77.0364")
	if calls != 2 {
		t.Errorf("want a fresh attempt once the backoff lapses, got %d attempts", calls)
	}
}

func TestDiscoverCapsTheResponse(t *testing.T) {
	evs := make([]ticketmaster.Event, 0, DiscoverMaxEvents+10)
	for i := range DiscoverMaxEvents + 10 {
		// Distinct days so each is its own event key rather than one bill.
		evs = append(evs, dcEvent(fmt.Sprintf("Act%d", i), discoverNow.Add(time.Duration(24*(i+1))*time.Hour)))
	}
	h, _ := newDiscover([][]byte{blobOf(t, evs...)})
	_, body := getDiscover(t, h, "lat=38.8951&lng=-77.0364&radius=50")
	if body.Count != DiscoverMaxEvents || len(body.Events) != DiscoverMaxEvents {
		t.Fatalf("want the response capped at %d, got %d", DiscoverMaxEvents, body.Count)
	}
	// Capped from the front of a date-sorted list: the soonest shows are the
	// ones worth showing a visitor.
	if !body.Events[0].Date.Before(body.Events[1].Date) {
		t.Errorf("events are not date-ordered: %v then %v", body.Events[0].Date, body.Events[1].Date)
	}
}

// Nothing in this response may look personalised. An artist id here would be
// a Ticketmaster attraction wearing a Spotify field, and a client that used
// it to save or subscribe would be writing rows about an artist that does
// not exist.
func TestDiscoverActsCarryNoUserState(t *testing.T) {
	h, _ := newDiscover([][]byte{blobOf(t, dcEvent("Nearby", discoverNow.Add(30*time.Hour)))})
	rec, _ := getDiscover(t, h, "lat=38.8951&lng=-77.0364&radius=50")

	var body struct {
		Events []struct {
			Acts []map[string]json.RawMessage `json:"acts"`
		} `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	act := body.Events[0].Acts[0]
	for _, absent := range []string{"saved", "subscribed", "reason"} {
		if _, present := act[absent]; present {
			t.Errorf("discover act carries %q; this response knows nothing about the caller", absent)
		}
	}
	var artist struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(act["artist"], &artist); err != nil {
		t.Fatal(err)
	}
	if artist.ID != "" {
		t.Errorf("discover act carries an artist id (%q) — see FromCachedTicketmaster", artist.ID)
	}
}
