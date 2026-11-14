package concerts

import (
	"strings"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// Two users a few miles apart in one city must land on one cache key, or the
// "two users in the same cell cost one fetch" in docs/public-path.md §2.1
// never happens: un-snapped keys are exact to four decimals.
func TestGenreCellIsSharedWithinACell(t *testing.T) {
	a := Location{Latitude: 40.7128, Longitude: -74.0060, RadiusMiles: 50} // lower Manhattan
	b := Location{Latitude: 40.7812, Longitude: -73.9665, RadiusMiles: 50} // Central Park
	if ka, kb := GenreCacheKey("KnvZfZ7vAvE", GenreCell(a)), GenreCacheKey("KnvZfZ7vAvE", GenreCell(b)); ka != kb {
		t.Errorf("keys differ within one cell: %s vs %s", ka, kb)
	}
	cell := GenreCell(a)
	if cell.RadiusMiles != a.RadiusMiles+GenreFetchPadMiles {
		t.Errorf("radius = %d, want the user's plus the pad", cell.RadiusMiles)
	}
	// The pad must cover the farthest a user can sit from the cell centre,
	// half a cell's diagonal, or the edge of their circle is never fetched.
	if worst := 0.5 * GenreGridDegrees * 69 * 1.4143; float64(GenreFetchPadMiles) < worst {
		t.Errorf("pad %d mi is under the %.1f mi a user can sit from the cell centre", GenreFetchPadMiles, worst)
	}
	if !strings.HasPrefix(GenreCacheKey("g", cell), CachePrefixTicketmaster) {
		t.Error("genre keys must be tm: rows, or the janitor never expires them")
	}
	far := Location{Latitude: 39.7392, Longitude: -104.9903, RadiusMiles: 50}
	if GenreCacheKey("g", GenreCell(far)) == GenreCacheKey("g", cell) {
		t.Error("Denver and New York share a key")
	}
}

func dayConcert(day, artist string) Concert {
	t, _ := time.Parse("2006-01-02", day)
	return Concert{Artist: ArtistRef{Name: artist}, Date: t.Add(20 * time.Hour), LocalDate: day, Venue: "V", City: "C"}
}

// The genre feed is complete before its horizon and holds nothing on or
// after it, and the cap pulls the horizon earlier on a day boundary.
func TestCapGenreFeed(t *testing.T) {
	var cs []Concert
	start := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ { // 10 shows a day for 5 days
		cs = append(cs, dayConcert(start.AddDate(0, 0, i/10).Format("2006-01-02"), string(rune('a'+i%26))+strings.Repeat("x", i)))
	}

	got, h := capGenreFeed(append([]Concert(nil), cs...), "2026-12-04", 100)
	if len(got) != 30 || h != "2026-12-04" {
		t.Errorf("listing horizon: kept %d with horizon %s, want 30 before 2026-12-04", len(got), h)
	}

	got, h = capGenreFeed(append([]Concert(nil), cs...), "", 25)
	if len(got) != 20 || h != "2026-12-03" {
		t.Errorf("cap: kept %d with horizon %s, want 20 (whole days) before 2026-12-03", len(got), h)
	}
	for _, c := range got {
		if c.LocalDay() >= h {
			t.Errorf("row on %s survived a horizon of %s", c.LocalDay(), h)
		}
	}

	got, h = capGenreFeed(append([]Concert(nil), cs...), "", 100)
	if len(got) != 50 || h != "" {
		t.Errorf("under the cap: kept %d horizon %q, want everything and no horizon", len(got), h)
	}

	// One day busier than the cap: no boundary to cut at, so the cap holds.
	got, _ = capGenreFeed(append([]Concert(nil), cs[:10]...), "", 4)
	if len(got) != 4 {
		t.Errorf("single dense day: kept %d, want the cap of 4", len(got))
	}
}

// A genre listing's acts come from Ticketmaster's lineup: an attraction gets
// a tm: ID so the bell works, the genre is the event's own, and an event with
// no attraction is named after the event.
func TestGenreConcerts(t *testing.T) {
	g := GenreTarget{ID: "KnvZfZ7v7nE", Name: "Country"}
	e := ticketmaster.Event{
		Name: "Luke Combs with Special Guests", URL: "https://tm/e", Start: time.Now(), LocalDate: "2026-12-01",
		Lineup: []ticketmaster.Attraction{{ID: "K1", Name: "Luke Combs"}, {ID: "K2", Name: "Opener"}},
		Venue:  ticketmaster.Venue{Name: "Ball Arena", City: "Denver", Latitude: 39.7, Longitude: -105},
		Genre:  "Country",
	}
	cs := genreConcerts(e, g)
	if len(cs) != 2 || cs[0].Artist.ID != "tm:K1" || cs[0].Artist.Genres[0] != "Country" {
		t.Fatalf("concerts = %+v", cs)
	}
	e.Lineup, e.Genre = nil, ""
	cs = genreConcerts(e, g)
	if len(cs) != 1 || cs[0].Artist.Name != e.Name || cs[0].Artist.ID != "" || cs[0].Artist.Genres[0] != "Country" {
		t.Errorf("no-attraction event = %+v, want one act named after the event, no ID, the picked genre", cs)
	}
}
