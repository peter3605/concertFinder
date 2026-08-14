package http

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
)

// A show that already happened is not an upcoming concert. Nothing used to
// enforce that: the snapshot is rebuilt at most every few hours, the janitor
// keeps past events for another week, and no layer in between applied a lower
// date bound — so last night's show sat at the top of a list headed "Upcoming
// concerts" until the next scan replaced it.
func TestParseFiltersHidesPastShowsByDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/me/concerts", nil)
	f := parseFilters(r)
	if f.DateFrom.IsZero() {
		t.Fatal("no default lower bound: past shows would be served")
	}

	yesterday := time.Now().UTC().Add(-24 * time.Hour)
	// Earlier today still counts as today — the floor is the start of the UTC
	// day, not time.Now(), so a matinee doesn't vanish from its own listing
	// halfway through the afternoon.
	earlierToday := startOfUTCDay(time.Now()).Add(time.Hour)
	tomorrow := time.Now().UTC().Add(24 * time.Hour)

	cs := []concerts.Concert{
		{Artist: concerts.ArtistRef{Name: "Old"}, Date: yesterday, Venue: "V", City: "C"},
		{Artist: concerts.ArtistRef{Name: "Matinee"}, Date: earlierToday, Venue: "V", City: "C"},
		{Artist: concerts.ArtistRef{Name: "Soon"}, Date: tomorrow, Venue: "V", City: "C"},
	}
	got := concerts.Apply(cs, f)
	if len(got) != 2 {
		t.Fatalf("expected yesterday dropped and today+tomorrow kept, got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if c.Artist.Name == "Old" {
			t.Error("a show from yesterday survived the default filter")
		}
	}
}

// An explicit date_from still wins — the floor is a default, not a policy.
func TestParseFiltersExplicitDateFromOverridesTheFloor(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/me/concerts?date_from=2020-01-01", nil)
	f := parseFilters(r)
	want := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if !f.DateFrom.Equal(want) {
		t.Errorf("DateFrom = %v, want %v", f.DateFrom, want)
	}
}

// Facets are computed after the past-show floor is applied. Counting the
// unfiltered snapshot would promise cards that no view of this list can
// produce — the same "the number on the chip must be the number you get"
// invariant the genre and venue facets already hold.
func TestFacetsExcludePastShows(t *testing.T) {
	floor := concerts.Filters{DateFrom: startOfUTCDay(time.Now())}
	mk := func(name string, when time.Time) concerts.Concert {
		c := concerts.Concert{Artist: concerts.ArtistRef{Name: name, Genres: []string{"rock"}},
			Date: when, Venue: "Union Stage", City: "Washington"}
		return c
	}
	cs := []concerts.Concert{
		mk("Gone", time.Now().UTC().Add(-48*time.Hour)),
		mk("Coming", time.Now().UTC().Add(48*time.Hour)),
	}
	facets := computeFacets(concerts.Apply(cs, floor))
	for _, f := range facets.Genres {
		if f.Value == "rock" && f.Count != 1 {
			t.Errorf("rock facet counts a show that already happened: got %d, want 1", f.Count)
		}
	}
	for _, f := range facets.Venues {
		if f.Count != 1 {
			t.Errorf("venue facet counts a show that already happened: got %d, want 1", f.Count)
		}
	}
}
