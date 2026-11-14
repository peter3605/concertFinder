package ticketmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// Decoded events carry Ticketmaster's genre, from the primary
// classification, so a genre listing's cards facet under Ticketmaster's word
// (CF-31). The secondary classification's Pop must not win over the primary
// Rock.
func TestDecodedEventsCarryThePrimaryGenre(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "events_festival.json"))
	evs, _, err := c.SearchEvents(context.Background(), "K8vZ917headline", testLat, testLng, testRadius, nil)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	byID := eventsByID(evs)
	for id, want := range map[string]string{
		"vvG1zZ9festmark":  "Rock",
		"vvG1zZ9secondary": "Rock",
		"vvG1zZ9noclass":   "",
	} {
		if got := byID[id].Genre; got != want {
			t.Errorf("%s: Genre = %q, want %q", id, got, want)
		}
	}
}

// genreEvent is one row of a synthetic genre listing.
func genreEvent(i int, day string, attractions ...map[string]any) map[string]any {
	ev := map[string]any{
		"id":   fmt.Sprintf("ev%d", i),
		"name": fmt.Sprintf("Show %d", i),
		"url":  fmt.Sprintf("https://www.ticketmaster.com/event/%d", i),
		"dates": map[string]any{
			"start":    map[string]any{"localDate": day, "localTime": "20:00:00"},
			"timezone": "America/New_York",
		},
		"classifications": []any{map[string]any{
			"primary": true,
			"segment": map[string]any{"id": MusicSegmentID, "name": "Music"},
			"genre":   map[string]any{"id": "KnvZfZ7vAvE", "name": "Jazz"},
		}},
		"_embedded": map[string]any{
			"venues": []any{map[string]any{
				"name":     "Blue Note",
				"city":     map[string]any{"name": "New York"},
				"location": map[string]any{"latitude": "40.7306", "longitude": "-74.0004"},
			}},
			"attractions": attractions,
		},
	}
	return ev
}

// A genre listing is fetched date ascending and stopped at MaxGenrePages,
// and that cut is reported as cacheable with the last row's day as its
// horizon: a date-sorted prefix is correct up to there (docs/public-path.md
// §1.2). New York jazz is 16 pages; this one is 6.
func TestSearchGenreEventsStopsAtThreePagesWithAHorizon(t *testing.T) {
	const totalPages = 6
	start := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	c, rec := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var events []any
		for i := 0; i < 100; i++ {
			n := page*100 + i
			day := start.AddDate(0, 0, n/10).Format("2006-01-02")
			events = append(events, genreEvent(n, day, map[string]any{"id": fmt.Sprintf("K%d", n), "name": fmt.Sprintf("Act %d", n)}))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"_embedded": map[string]any{"events": events},
			"page":      map[string]any{"size": 100, "totalElements": totalPages * 100, "totalPages": totalPages, "number": page},
		})
	})
	permits := 0
	evs, horizon, cacheable, err := c.SearchGenreEvents(context.Background(), "KnvZfZ7vAvE", 40.75, -74.0, 65, func() bool { permits++; return true })
	if err != nil {
		t.Fatalf("SearchGenreEvents: %v", err)
	}
	if got := rec.count(); got != MaxGenrePages {
		t.Errorf("requests = %d, want %d", got, MaxGenrePages)
	}
	if permits != MaxGenrePages-1 {
		t.Errorf("page permits asked = %d, want %d (one per page after the first)", permits, MaxGenrePages-1)
	}
	if len(evs) != 300 {
		t.Errorf("events = %d, want 300", len(evs))
	}
	if !cacheable {
		t.Error("a listing cut at its page cap must be cacheable: it is a correct date-sorted prefix")
	}
	if want := evs[len(evs)-1].LocalDate; horizon != want || horizon == "" {
		t.Errorf("horizon = %q, want the last row's day %q", horizon, want)
	}
	q := rec.requests()[0].Query()
	assertQuery(t, q, "genreId", "KnvZfZ7vAvE")
	assertQuery(t, q, "sort", "date,asc")
	assertQuery(t, q, "classificationName", "Music")
	if evs[0].Genre != "Jazz" {
		t.Errorf("Genre = %q, want Jazz", evs[0].Genre)
	}
}

// A refused page permit leaves an arbitrary fragment, not the prefix that was
// asked for, so it must not be cached. A listing that ends inside the cap is
// whole: no horizon.
func TestSearchGenreEventsCacheability(t *testing.T) {
	serve := func(totalPages int) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"_embedded": map[string]any{"events": []any{genreEvent(page, fmt.Sprintf("2026-12-%02d", page+1))}},
				"page":      map[string]any{"totalPages": totalPages, "number": page},
			})
		}
	}
	c, _ := newTestAPI(t, serve(5))
	_, horizon, cacheable, err := c.SearchGenreEvents(context.Background(), "g", 40.75, -74.0, 65, func() bool { return false })
	if err != nil || cacheable || horizon == "" {
		t.Errorf("refused permit: cacheable=%v horizon=%q err=%v, want not cacheable with a horizon", cacheable, horizon, err)
	}

	c, _ = newTestAPI(t, serve(2))
	_, horizon, cacheable, err = c.SearchGenreEvents(context.Background(), "g", 40.75, -74.0, 65, func() bool { return true })
	if err != nil || !cacheable || horizon != "" {
		t.Errorf("whole listing: cacheable=%v horizon=%q err=%v, want cacheable with no horizon", cacheable, horizon, err)
	}
}

// Seat packages are not acts. Denver's country listing returned "Club Level
// Seating: Ball Arena" as an attraction; dropped from the lineup, and an
// event that is nothing but a package is dropped whole.
func TestSeatPackagesAreExcluded(t *testing.T) {
	c, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		withPackage := genreEvent(1, "2026-12-01",
			map[string]any{"id": "Kreal", "name": "Luke Combs"},
			map[string]any{"id": "Kseat", "name": "Club Level Seating: Ball Arena"},
		)
		packageOnly := genreEvent(2, "2026-12-02", map[string]any{"id": "Kseat", "name": "Club Level Seating: Ball Arena"})
		packageOnly["name"] = "Club Level Seating: Ball Arena"
		noAttraction := genreEvent(3, "2026-12-03")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"_embedded": map[string]any{"events": []any{withPackage, packageOnly, noAttraction}},
			"page":      map[string]any{"totalPages": 1, "number": 0},
		})
	})
	evs, _, _, err := c.SearchGenreEvents(context.Background(), "g", 39.75, -105.0, 65, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := eventsByID(evs)
	if _, ok := byID["ev2"]; ok {
		t.Error("an event that is only a seat package survived decoding")
	}
	if l := byID["ev1"].Lineup; len(l) != 1 || l[0].Name != "Luke Combs" {
		t.Errorf("lineup = %+v, want only Luke Combs", l)
	}
	// An ordinary show with no attraction is kept; the feed names it after
	// the event.
	if _, ok := byID["ev3"]; !ok {
		t.Error("an event with no attraction was dropped")
	}

	for name, want := range map[string]bool{
		"Club Level Seating: Ball Arena": true,
		"Premium Seats - Madison Square": true,
		"Parking: Red Rocks":             true,
		"Parkway Drive":                  false,
		"Suicideboys":                    false,
		"Seatbelts":                      false,
		"Phoebe Bridgers":                false,
	} {
		if got := IsSeatPackage(name); got != want {
			t.Errorf("IsSeatPackage(%q) = %v, want %v", name, got, want)
		}
	}
}

// The catalog is the Music segment's top-level genres, without Undefined.
func TestMusicGenres(t *testing.T) {
	c, rec := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": MusicSegmentID, "name": "Music",
			"_embedded": map[string]any{"genres": []any{
				map[string]any{"id": "KnvZfZ7vAvE", "name": "Jazz"},
				map[string]any{"id": "KnvZfZ7vAeA", "name": "Rock"},
				map[string]any{"id": "KnvZfZ7vAvl", "name": "Undefined"},
				map[string]any{"id": "KnvZfZ7vAv6", "name": "Country"},
			}},
		})
	})
	gs, err := c.MusicGenres(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 3 || gs[0].Name != "Country" || gs[2].Name != "Rock" {
		t.Errorf("genres = %+v, want Country, Jazz, Rock", gs)
	}
	if p := rec.requests()[0].Path; p != "/discovery/v2/classifications/segments/"+MusicSegmentID+".json" {
		t.Errorf("path = %s", p)
	}
}
