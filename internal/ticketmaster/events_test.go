package ticketmaster

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSearchEventsParsesStartAcrossTimezonesAndTBA(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "events_dates.json"))
	evs, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
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
	evs, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
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
	evs, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
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
	evs, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
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
	evs, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
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
	evs, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
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
	if _, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil); err != nil {
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
	evs, _, err := c.SearchEvents(context.Background(), "", testLat, testLng, testRadius, nil)
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
	_, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
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
	_, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
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

// --- pagination (CF-B3) ---------------------------------------------------

// pagedEvents serves a synthetic result set spread over totalPages, reading
// the requested page from the query string. Generated rather than kept in
// testdata because these assertions are about how many requests the client
// makes and what it asks for, not about decoding a recorded body -- and a
// ten-page fixture set would be ten near-identical files.
func pagedEvents(t *testing.T, totalPages, perPage int) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		page := 0
		if p := r.URL.Query().Get("page"); p != "" {
			var err error
			if page, err = strconv.Atoi(p); err != nil {
				t.Errorf("page parameter %q is not a number", p)
			}
		}
		var b strings.Builder
		fmt.Fprint(&b, `{"_embedded":{"events":[`)
		for i := 0; i < perPage; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"p%de%d","name":"Show %d-%d",`, page, i, page, i)
			fmt.Fprint(&b, `"dates":{"start":{"dateTime":"2026-09-16T03:00:00Z"}},`)
			fmt.Fprint(&b, `"_embedded":{"venues":[{"name":"Test Room","city":{"name":"Washington"}}]}}`)
		}
		fmt.Fprintf(&b, `]},"page":{"size":100,"totalElements":%d,"totalPages":%d,"number":%d}}`,
			totalPages*perPage, totalPages, page)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(b.String()))
	}
}

// allowAll is a PagePermit that never refuses, plus a counter. The count is
// the assertion that matters most in this file: it is how many permits the
// production call site would have spent.
func allowAll(n *int) PagePermit {
	return func() bool { *n++; return true }
}

// The bug this story fixes: size=100 with no page parameter meant an
// attraction with more than one page of events in radius had the remainder
// dropped with no error and no log. A residency or a festival act is the real
// case; the failure presented as a short listing that looked complete.
func TestSearchEventsFollowsPagination(t *testing.T) {
	c, rec := newTestAPI(t, pagedEvents(t, 3, 2))
	permits := 0
	evs, complete, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, allowAll(&permits))
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if !complete {
		t.Error("complete = false, want true -- every page was fetched")
	}
	if len(evs) != 6 {
		t.Fatalf("got %d events across 3 pages of 2, want 6", len(evs))
	}
	if n := rec.count(); n != 3 {
		t.Fatalf("made %d requests, want 3 (one per page)", n)
	}
	// Page 0 must not carry a page parameter: the first request stays
	// byte-for-byte what it was before pagination existed.
	if q := rec.requests()[0].Query(); q.Has("page") {
		t.Errorf("first request carried page=%q; it should be omitted", q.Get("page"))
	}
	for i, want := range []string{"1", "2"} {
		if got := rec.requests()[i+1].Query().Get("page"); got != want {
			t.Errorf("request %d asked for page=%q, want %q", i+2, got, want)
		}
	}
	// Every page goes through the same decoder, so a later page's events are
	// as fully populated as the first one's.
	byID := eventsByID(evs)
	last, ok := byID["p2e1"]
	if !ok {
		t.Fatal("the last event of the last page is missing")
	}
	if last.Venue.Name != "Test Room" || last.Venue.City != "Washington" {
		t.Errorf("page 2 event decoded thinly: venue=%q city=%q", last.Venue.Name, last.Venue.City)
	}
	if last.Start.IsZero() {
		t.Error("page 2 event has no start time")
	}
}

// Quota is charged per upstream request. Every page after the first asks the
// permit exactly once, which is what keeps RATE_CAP_TM_* meaning the number
// it states -- the Songkick two-request lookup charging a single permit is
// the precedent this avoids repeating.
func TestSearchEventsChargesOnePermitPerExtraPage(t *testing.T) {
	c, rec := newTestAPI(t, pagedEvents(t, 4, 1))
	permits := 0
	if _, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, allowAll(&permits)); err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	// 4 requests, of which the caller pre-paid the first.
	if rec.count() != 4 {
		t.Fatalf("made %d requests, want 4", rec.count())
	}
	if permits != 3 {
		t.Errorf("asked the permit %d times, want 3 (one per request after the first)", permits)
	}
}

// A refused permit is the daily cap being reached mid-artist. It must keep the
// pages already paid for and report the result incomplete -- never discard
// them, and never report success. complete=false is what stops the caller
// caching a truncated set for the full 12h TTL.
func TestSearchEventsStopsAndReportsIncompleteWhenThePermitRefuses(t *testing.T) {
	c, rec := newTestAPI(t, pagedEvents(t, 5, 2))
	calls := 0
	permit := func() bool { calls++; return calls <= 1 } // allow page 1, refuse page 2
	evs, complete, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, permit)
	if err != nil {
		t.Fatalf("a refused permit is not an error: %v", err)
	}
	if complete {
		t.Error("complete = true after pagination was cut short; the caller would cache a truncated result")
	}
	if rec.count() != 2 {
		t.Fatalf("made %d requests, want 2 (page 0 pre-paid, page 1 permitted, page 2 refused)", rec.count())
	}
	if len(evs) != 4 {
		t.Errorf("got %d events, want 4 -- the pages already fetched must be kept", len(evs))
	}
}

// A nil permit fetches the first page only. The safe default direction: an
// unpermitted caller under-reads one artist rather than silently overspending
// an allowance shared by every user of the deployment. It is still not silent
// -- complete is false.
func TestSearchEventsWithNilPermitFetchesOnePageAndSaysSo(t *testing.T) {
	c, rec := newTestAPI(t, pagedEvents(t, 3, 2))
	evs, complete, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if complete {
		t.Error("complete = true with 2 pages left unfetched")
	}
	if rec.count() != 1 {
		t.Errorf("made %d requests with a nil permit, want 1", rec.count())
	}
	if len(evs) != 2 {
		t.Errorf("got %d events, want the 2 on page 0", len(evs))
	}
}

// A single-page result is complete and costs exactly one request. This is the
// overwhelmingly common case -- an ordinary touring artist -- and it must not
// have become more expensive: CallsPerArtistColdScan (2) is sized on it.
func TestSearchEventsSinglePageIsCompleteAndCostsOneRequest(t *testing.T) {
	c, rec := newTestAPI(t, pagedEvents(t, 1, 3))
	permits := 0
	evs, complete, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, allowAll(&permits))
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if !complete {
		t.Error("a single-page result must report complete")
	}
	if rec.count() != 1 || permits != 0 {
		t.Errorf("made %d requests and asked %d permits, want 1 and 0", rec.count(), permits)
	}
	if len(evs) != 3 {
		t.Errorf("got %d events, want 3", len(evs))
	}
}

// totalPages is 0 on an empty result set. Reading that as "keep going" would
// loop to MaxEventPages spending a permit each time, for an artist with no
// shows at all -- the most common outcome of a cold scan.
func TestSearchEventsEmptyResultDoesNotPaginate(t *testing.T) {
	c, rec := newTestAPI(t, serveFixture(t, "events_empty.json"))
	permits := 0
	evs, complete, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, allowAll(&permits))
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if !complete {
		t.Error("an empty result set is complete, not truncated")
	}
	if len(evs) != 0 {
		t.Errorf("got %d events, want 0", len(evs))
	}
	if rec.count() != 1 || permits != 0 {
		t.Errorf("made %d requests and asked %d permits on an empty result, want 1 and 0", rec.count(), permits)
	}
}

// MaxEventPages is the Discovery API's own deep-paging ceiling (page*size <=
// 1000). A server claiming more pages than that must not drag the client past
// it: page 10 would spend a permit to be refused by TM.
func TestSearchEventsStopsAtMaxEventPages(t *testing.T) {
	c, rec := newTestAPI(t, pagedEvents(t, 999, 1))
	permits := 0
	_, complete, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, allowAll(&permits))
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if complete {
		t.Error("complete = true after stopping at the page bound")
	}
	if rec.count() != MaxEventPages {
		t.Errorf("made %d requests, want MaxEventPages (%d)", rec.count(), MaxEventPages)
	}
}

// A later page failing keeps the earlier pages. Discarding them would throw
// away results already paid for in quota, and would turn a partial answer into
// "TM has nothing" -- which escalates the artist into the far more expensive
// Phase 2 fallback chain.
func TestSearchEventsKeepsEarlierPagesWhenALaterOneFails(t *testing.T) {
	serve := pagedEvents(t, 4, 2)
	c, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		serve(w, r)
	})
	permits := 0
	evs, complete, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, allowAll(&permits))
	if err == nil {
		t.Fatal("a failed page must be reported, not swallowed")
	}
	if complete {
		t.Error("complete = true despite a failed page")
	}
	if len(evs) != 4 {
		t.Errorf("got %d events, want the 4 from pages 0 and 1", len(evs))
	}
}

// The acceptance criterion for CF-B3, stated literally: more than 100 events
// in radius are no longer silently truncated. Two full pages at the API's
// maximum size is the shape the bug actually took -- size=100 with no page
// parameter returned exactly the first 100 and reported nothing amiss.
func TestSearchEventsReturnsMoreThanOnePageOfEvents(t *testing.T) {
	c, rec := newTestAPI(t, pagedEvents(t, 2, eventsPageSize))
	permits := 0
	evs, complete, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, allowAll(&permits))
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if !complete {
		t.Error("complete = false; both pages were available and permitted")
	}
	if len(evs) <= eventsPageSize {
		t.Fatalf("got %d events, want more than one page (%d) -- this is the truncation CF-B3 fixes", len(evs), eventsPageSize)
	}
	if len(evs) != 2*eventsPageSize {
		t.Errorf("got %d events, want %d", len(evs), 2*eventsPageSize)
	}
	if rec.count() != 2 || permits != 1 {
		t.Errorf("made %d requests having asked %d permits, want 2 and 1", rec.count(), permits)
	}
}
