package ticketmaster

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSearchEventsParsesStartAcrossTimezonesAndTBA(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "events_dates.json"))
	evs, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	byID := eventsByID(evs)

	for _, tc := range []struct {
		name string
		id   string
		want time.Time
		why  string
	}{
		{
			name: "an absolute dateTime is taken as the instant it names",
			id:   "vvG1zZ9pacific",
			want: time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC),
			why:  "20:00 Pacific on 2026-09-15 is 03:00Z the next day; TM reports it in Z and we keep it",
		},
		{
			name: "a matinee does not roll over the UTC day",
			id:   "vvG1zZ9matinee",
			want: time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC),
			why:  "14:00 Eastern stays on its own UTC day, unlike the evening shows beside it",
		},
		{
			name: "a TBA time falls back to localDate at UTC midnight",
			id:   "vvG1zZ9timetba",
			want: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
			why:  "timeTBA events carry no dateTime at all, so localDate is the only signal",
		},
		{
			name: "an undecodable dateTime falls back to localDate rather than dropping the show",
			id:   "vvG1zZ9malformed",
			want: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
			why:  "the RFC3339 parse error is swallowed on purpose; a bad time must not cost us the date",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := byID[tc.id]
			if !ok {
				t.Fatalf("event %s missing from results (%s)", tc.id, tc.why)
			}
			if !got.Start.Equal(tc.want) {
				t.Errorf("Start = %s, want %s (%s)", got.Start.UTC().Format(time.RFC3339), tc.want.Format(time.RFC3339), tc.why)
			}
		})
	}

	// dateTBD: no dateTime and no localDate. There is nothing to sort, group
	// or filter such a row by, so it is dropped rather than defaulting to the
	// zero time -- which would land in January year 1 and sit permanently at
	// the top of a list sorted ascending by date.
	if _, ok := byID["vvG1zZ9datetbd"]; ok {
		t.Error("an event with no usable date must be dropped, not emitted with a zero Start")
	}
	if len(evs) != 4 {
		t.Errorf("expected 4 datable events out of 5, got %d", len(evs))
	}
}

// Start is an instant, not a calendar day, and the two disagree for almost
// every US evening show. concerts.DedupKey and concerts.EventKey both bucket
// by date.UTC().Format("2006-01-02"), so the Pacific event below is keyed to
// 2026-09-16 while Ticketmaster's own localDate for it is 2026-09-15.
//
// This test pins the behaviour rather than blessing it: the divergence is
// reported separately, and the fix (if there is one) belongs downstream in
// concerts, not here -- this package's job is to report the instant the API
// gave us. What matters for a regression is that these two events, which are
// the same local calendar day at the same venue, currently land on different
// UTC days purely because one has a published time and the other does not.
func TestSearchEventsStartIsAnInstantNotALocalCalendarDay(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "events_dates.json"))
	evs, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	byID := eventsByID(evs)

	const day = "2006-01-02"
	timed := byID["vvG1zZ9pacific"].Start.UTC().Format(day)
	tba := byID["vvG1zZ9timetba"].Start.UTC().Format(day)

	if timed != "2026-09-16" {
		t.Errorf("timed Pacific show buckets to %s, want 2026-09-16 (TM localDate is 2026-09-15)", timed)
	}
	if tba != "2026-09-15" {
		t.Errorf("TBA show at the same venue buckets to %s, want 2026-09-15", tba)
	}
	if timed == tba {
		t.Error("this test has stopped describing the code; re-read the divergence it documents")
	}
}

func TestSearchEventsExtractsTheWholeLineupInOrder(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "events_lineup.json"))
	evs, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	byID := eventsByID(evs)

	for _, tc := range []struct {
		name string
		id   string
		want []Attraction
		why  string
	}{
		{
			name: "a three-act bill yields three acts in the order TM listed them",
			id:   "vvG1zZ9lineup3",
			want: []Attraction{
				{ID: "K8vZ917headline", Name: "Test Headliner"},
				{ID: "K8vZ917support1", Name: "Test Support One"},
				{ID: "K8vZ917support2", Name: "Test Support Two"},
			},
			why: "concerts.billingOf reads position, so reordering here silently reassigns headliner",
		},
		{
			name: "a solo booking yields exactly one act",
			id:   "vvG1zZ9lineup1",
			want: []Attraction{{ID: "K8vZ917headline", Name: "Test Headliner"}},
			why:  "the common case; no padding, no synthetic entries",
		},
		{
			name: "an attraction with no name is dropped from the lineup",
			id:   "vvG1zZ9nameless",
			want: []Attraction{{ID: "K8vZ917headline", Name: "Test Headliner"}},
			why:  "a blank name normalizes to the empty string, which billingOf would match against nothing while still shifting every real act one slot down",
		},
		{
			name: "an event with no attractions block yields an empty lineup",
			id:   "vvG1zZ9nolineup",
			want: []Attraction{},
			why:  "not every TM listing carries attractions; that is a missing lineup, not a missing event",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := byID[tc.id]
			if !ok {
				t.Fatalf("event %s missing from results", tc.id)
			}
			if len(got.Lineup) != len(tc.want) {
				t.Fatalf("Lineup = %+v, want %+v (%s)", got.Lineup, tc.want, tc.why)
			}
			for i := range tc.want {
				if got.Lineup[i] != tc.want[i] {
					t.Errorf("Lineup[%d] = %+v, want %+v (%s)", i, got.Lineup[i], tc.want[i], tc.why)
				}
			}
		})
	}

	// The lineup is what concerts.GroupEvents folds a festival by, so the
	// event still has to carry its own identifying fields alongside it.
	e := byID["vvG1zZ9lineup3"]
	if e.Name != "Test Headliner with Test Support" {
		t.Errorf("Name = %q", e.Name)
	}
	if e.URL != "https://www.ticketmaster.com/event/vvG1zZ9lineup3" {
		t.Errorf("URL = %q", e.URL)
	}
}

func TestSearchEventsReadsFestivalFromClassificationNotName(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "events_festival.json"))
	evs, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	byID := eventsByID(evs)

	for _, tc := range []struct {
		name string
		id   string
		want bool
		why  string
	}{
		{
			name: "subType Festival marks the event",
			id:   "vvG1zZ9festmark",
			want: true,
			why:  "TM's own classification is the only signal we trust",
		},
		{
			name: "subType Undefined does not",
			id:   "vvG1zZ9ordinary",
			want: false,
			why:  "the overwhelmingly common value; roughly 399 in 400 events look like this",
		},
		{
			name: "the word Festival in the event name does not",
			id:   "vvG1zZ9namedfest",
			want: false,
			why:  "IsFestival is documented as TM's classification, not a guess from the title -- a name heuristic would also flag every 'Festival of Lights' support slot",
		},
		{
			name: "a Festival subType on a non-primary classification still marks it",
			id:   "vvG1zZ9secondary",
			want: true,
			why:  "the loop scans every classification, and the lowercase spelling proves the comparison is EqualFold rather than ==",
		},
		{
			name: "an event with no classifications at all is not marked",
			id:   "vvG1zZ9noclass",
			want: false,
			why:  "false means 'not marked', never 'definitely not a festival'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := byID[tc.id]
			if !ok {
				t.Fatalf("event %s missing from results", tc.id)
			}
			if got.IsFestival != tc.want {
				t.Errorf("IsFestival = %v, want %v (%s)", got.IsFestival, tc.want, tc.why)
			}
		})
	}
}

func TestSearchEventsMapsVenueWithCodeFallbacks(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "events_venue.json"))
	evs, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	byID := eventsByID(evs)

	for _, tc := range []struct {
		name string
		id   string
		want Venue
		why  string
	}{
		{
			name: "codes win when TM supplies them",
			id:   "vvG1zZ9venuecode",
			want: Venue{Name: "Test Club", City: "Washington", State: "DC", Country: "US", Latitude: 38.9180, Longitude: -77.0234},
			why:  "the normal shape; stateCode/countryCode are what the UI and filters expect",
		},
		{
			name: "the full name stands in for a missing code",
			id:   "vvG1zZ9venuename",
			want: Venue{Name: "Test Provincial Hall", City: "Toronto", State: "Ontario", Country: "Canada", Latitude: 43.6532, Longitude: -79.3832},
			why:  "an empty State would erase the only regional label the card can show",
		},
		{
			name: "unparseable coordinates leave the venue at zero rather than failing the event",
			id:   "vvG1zZ9venuegeo",
			want: Venue{Name: "Test Pop-Up Room", City: "Brooklyn", State: "NY", Country: "US"},
			why:  "a geocode gap costs distance sorting, not the listing itself",
		},
		{
			name: "an event with no venue block still comes through",
			id:   "vvG1zZ9venuenone",
			want: Venue{},
			why:  "dedup normalizes an empty venue to the empty string, which is survivable; dropping the show is not",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := byID[tc.id]
			if !ok {
				t.Fatalf("event %s missing from results", tc.id)
			}
			if got.Venue != tc.want {
				t.Errorf("Venue = %+v, want %+v (%s)", got.Venue, tc.want, tc.why)
			}
		})
	}
}

// A response with no _embedded key at all is what TM returns for a search
// that matched nothing. It has to decode to an empty slice and a nil error:
// concerts.loadOrFetchTM caches this result and, crucially, an error here
// would be read as "TM failed" rather than "TM has nothing", which is the
// difference between escalating an artist to the Phase 2 fallback chain and
// not.
func TestSearchEventsHandlesAnEmptyResultSet(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "events_empty.json"))
	evs, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius)
	if err != nil {
		t.Fatalf("an empty result set is not an error: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("expected no events, got %d", len(evs))
	}
}

// The events query is filtered by attractionId, which is the whole point of
// resolving the attraction first: a keyword search returns cover bands and
// tribute acts under the artist's name.
func TestSearchEventsFiltersByAttractionIDNotKeyword(t *testing.T) {
	c, rec := newTestAPI(t, serveFixture(t, "events_empty.json"))
	if _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius); err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	reqs := rec.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(reqs))
	}
	got := reqs[0]
	if got.Path != "/discovery/v2/events.json" {
		t.Errorf("path = %q, want /discovery/v2/events.json", got.Path)
	}
	q := got.Query()
	for _, banned := range []string{"keyword", "attractionName", "artistName"} {
		if q.Has(banned) {
			t.Errorf("events query must not carry %q -- naive keyword search is what attraction resolution exists to avoid (got %q)", banned, q.Get(banned))
		}
	}
	assertQuery(t, q, "attractionId", "K8vZ917headline")
	assertQuery(t, q, "latlong", "38.9172,-77.0369")
	assertQuery(t, q, "radius", "50")
	assertQuery(t, q, "unit", "miles")
	assertQuery(t, q, "classificationName", "Music")
	assertQuery(t, q, "size", "100")
	assertQuery(t, q, "countryCode", "US")
	assertQuery(t, q, "apikey", secret)
}

// An empty attractionId means the caller has a negative resolution cached.
// Short-circuiting matters for cost, not correctness: every outbound TM call
// spends a permit from the user's daily rate cap, and at 200 artists a scan
// the unresolved ones are a large share of them.
func TestSearchEventsSkipsTheCallWhenThereIsNoAttractionID(t *testing.T) {
	c, rec := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("SearchEvents contacted the API with an empty attractionId")
		w.WriteHeader(http.StatusInternalServerError)
	})
	evs, err := c.SearchEvents(context.Background(), "", testLat, testLng, testRadius)
	if err != nil {
		t.Fatalf("an empty attractionId is not an error: %v", err)
	}
	if evs != nil {
		t.Errorf("expected nil events, got %+v", evs)
	}
	if rec.count() != 0 {
		t.Errorf("expected 0 requests, got %d", rec.count())
	}
}

// SearchEvents wraps doGETRetry's error with "tm events:", and
// concerts/search.go logs the result verbatim. The redaction has to survive
// that wrapping on the public path, not just inside doGETRetry.
func TestSearchEventsErrorDoesNotLeakTheAPIKey(t *testing.T) {
	c, _ := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})
	_, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("api key leaked into error: %q", err)
	}
	if !strings.Contains(err.Error(), "/discovery/v2/events.json") {
		t.Errorf("path should survive redaction: %q", err)
	}
}

// A malformed body is a decode failure, not an empty result. Returning
// (nil, nil) here would cache "no shows" for this artist for the full
// CONCERT_CACHE_TTL_HOURS window.
func TestSearchEventsReportsAMalformedBody(t *testing.T) {
	c, _ := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	})
	_, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius)
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if !strings.Contains(err.Error(), "decode tm events") {
		t.Errorf("error should name the decode step: %q", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("api key leaked into error: %q", err)
	}
}
