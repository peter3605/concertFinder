package http

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
)

func atVenue(name, venue string, day int) concerts.Concert {
	return concerts.Concert{
		Artist: concerts.ArtistRef{Name: name},
		Date:   time.Date(2026, 8, day, 20, 0, 0, 0, time.UTC),
		Venue:  venue,
		City:   "Washington",
	}
}

// The invariant that matters for any facet: the number on the chip has to
// equal the number of results you get when you click it. Genre facets got
// this wrong once by counting exact tags while filtering on substrings.
func TestVenueFacetCountsMatchWhatFilteringReturns(t *testing.T) {
	cs := []concerts.Concert{
		atVenue("A", "9:30 CLUB", 1),
		atVenue("B", "9:30 Club", 2), // same room, different feed's spelling
		atVenue("C", "The Anthem", 3),
		atVenue("D", "Anthem", 4), // ditto
		atVenue("E", "Union Stage", 5),
	}
	facets := computeFacets(cs)

	if len(facets.Venues) != 3 {
		t.Fatalf("expected 3 distinct venues after normalization, got %d: %+v",
			len(facets.Venues), facets.Venues)
	}
	for _, f := range facets.Venues {
		got := concerts.Apply(cs, concerts.Filters{Venue: f.Value})
		if len(got) != f.Count {
			t.Errorf("venue %q: facet says %d, filtering returns %d",
				f.Value, f.Count, len(got))
		}
	}
}

func TestVenueFacetsSortedByCountThenName(t *testing.T) {
	cs := []concerts.Concert{
		atVenue("A", "Union Stage", 1),
		atVenue("B", "9:30 Club", 2),
		atVenue("C", "9:30 Club", 3),
		atVenue("D", "The Anthem", 4),
	}
	got := computeFacets(cs).Venues
	if len(got) != 3 || got[0].Value != "9:30 Club" || got[0].Count != 2 {
		t.Fatalf("busiest venue should lead: %+v", got)
	}
	// Equal counts fall back to alphabetical so labels don't shuffle between
	// requests.
	if got[1].Value != "The Anthem" || got[2].Value != "Union Stage" {
		t.Errorf("ties should break alphabetically, got %+v", got[1:])
	}
}

func TestVenueFacetPicksTheCommonestSpelling(t *testing.T) {
	cs := []concerts.Concert{
		atVenue("A", "9:30 Club", 1),
		atVenue("B", "9:30 Club", 2),
		atVenue("C", "9:30 CLUB", 3),
	}
	got := computeFacets(cs).Venues
	if len(got) != 1 {
		t.Fatalf("expected one venue, got %+v", got)
	}
	if got[0].Value != "9:30 Club" {
		t.Errorf("should display the majority spelling, got %q", got[0].Value)
	}
	if got[0].Count != 3 {
		t.Errorf("count should span every spelling, got %d", got[0].Count)
	}
}

func TestVenueFacetsSkipBlankVenues(t *testing.T) {
	cs := []concerts.Concert{
		atVenue("A", "Union Stage", 1),
		atVenue("B", "", 2),
		atVenue("C", "   ", 3),
	}
	got := computeFacets(cs).Venues
	if len(got) != 1 || got[0].Value != "Union Stage" {
		t.Errorf("blank venues must not become a facet, got %+v", got)
	}
}

func TestGenreFacetCountsMatchWhatFilteringReturns(t *testing.T) {
	mk := func(name string, genres ...string) concerts.Concert {
		c := atVenue(name, "Union Stage", 1)
		c.Artist.Genres = genres
		return c
	}
	cs := []concerts.Concert{
		mk("A", "rock"),
		mk("B", "post-rock"),
		mk("C", "rock", "jazz"),
	}
	facets := computeFacets(cs)
	for _, f := range facets.Genres {
		got := concerts.Apply(cs, concerts.Filters{Genre: f.Value})
		if len(got) != f.Count {
			t.Errorf("genre %q: facet says %d, filtering returns %d",
				f.Value, f.Count, len(got))
		}
	}
}

// The saved list must be independent of the concert feed. It used to be
// `GET /me/concerts?saved_only=true`, which filtered the user's snapshot —
// so a save survived only while its artist stayed in the affinity top-200,
// and vanished silently on the next recompute. These assert the shape the
// dedicated endpoint relies on.
func TestAssemblePreservesQueryOrder(t *testing.T) {
	// The saved query sorts by event_date in SQL; Assemble must not reorder.
	in := []concerts.Concert{
		atVenue("C", "Union Stage", 3),
		atVenue("A", "9:30 Club", 1),
		atVenue("B", "The Anthem", 2),
	}
	blobs := make([][]byte, 0, len(in))
	for _, c := range in {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		blobs = append(blobs, b)
	}
	got, err := concerts.Assemble(blobs)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	for i := range in {
		if got[i].Artist.Name != in[i].Artist.Name {
			t.Errorf("position %d: got %q, want %q", i, got[i].Artist.Name, in[i].Artist.Name)
		}
	}
}

func TestAssembleEmptyIsNotAnError(t *testing.T) {
	got, err := concerts.Assemble(nil)
	if err != nil {
		t.Fatalf("no saves is a normal state, not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}
