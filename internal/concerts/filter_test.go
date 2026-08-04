package concerts

import (
	"testing"
	"time"
)

func mk(day int, genres []string) Concert {
	return Concert{
		Artist: ArtistRef{Name: "X", Genres: genres},
		Date:   time.Date(2026, 8, day, 20, 0, 0, 0, time.UTC),
		Venue:  "v", City: "c",
	}
}

func TestApply_GenreExactCaseInsensitive(t *testing.T) {
	cs := []Concert{mk(1, []string{"post-rock"}), mk(2, []string{"jazz"}), mk(3, nil)}
	got := Apply(cs, Filters{Genre: "POST-ROCK"})
	if len(got) != 1 || got[0].Artist.Genres[0] != "post-rock" {
		t.Errorf("got %+v", got)
	}
}

func TestApply_GenreDoesNotMatchSubstring(t *testing.T) {
	// The facet pills carry exact tag counts; a "rock · 1" pill that
	// returned every *-rock show made those counts a lie.
	cs := []Concert{mk(1, []string{"post-rock"}), mk(2, []string{"rock"})}
	got := Apply(cs, Filters{Genre: "rock"})
	if len(got) != 1 || got[0].Artist.Genres[0] != "rock" {
		t.Errorf("expected only the exact 'rock' tag, got %+v", got)
	}
}

func TestApply_GenreMatchesAnyOfSeveralTags(t *testing.T) {
	cs := []Concert{mk(1, []string{"indie", "shoegaze"}), mk(2, []string{"jazz"})}
	got := Apply(cs, Filters{Genre: "shoegaze"})
	if len(got) != 1 || got[0].Date.Day() != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestApply_DateRange(t *testing.T) {
	cs := []Concert{mk(1, nil), mk(5, nil), mk(10, nil)}
	got := Apply(cs, Filters{
		DateFrom: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
		DateTo:   time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC),
	})
	if len(got) != 1 || got[0].Date.Day() != 5 {
		t.Errorf("got %+v", got)
	}
}

func TestApply_WeekdayOnly(t *testing.T) {
	// 2026-08-03 Mon, 2026-08-08 Sat
	cs := []Concert{mk(3, nil), mk(8, nil)}
	got := Apply(cs, Filters{Weekday: WeekdayWeekday})
	if len(got) != 1 || got[0].Date.Day() != 3 {
		t.Errorf("weekday-only wrong: %+v", got)
	}
	got = Apply(cs, Filters{Weekday: WeekdayWeekend})
	if len(got) != 1 || got[0].Date.Day() != 8 {
		t.Errorf("weekend-only wrong: %+v", got)
	}
}

func TestApply_VenueExactUnderNormalization(t *testing.T) {
	// Sources disagree on case and punctuation for the same room; the
	// filter has to see through that or the facet counts lie.
	cs := []Concert{
		{Artist: ArtistRef{Name: "A"}, Date: time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC), Venue: "9:30 CLUB", City: "Washington"},
		{Artist: ArtistRef{Name: "B"}, Date: time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC), Venue: "9:30 Club", City: "Washington"},
		{Artist: ArtistRef{Name: "C"}, Date: time.Date(2026, 8, 3, 20, 0, 0, 0, time.UTC), Venue: "The Anthem", City: "Washington"},
	}
	got := Apply(cs, Filters{Venue: "9:30 club"})
	if len(got) != 2 {
		t.Errorf("expected both spellings of the same venue, got %d: %+v", len(got), got)
	}
}

func TestApply_VenueIgnoresLeadingArticle(t *testing.T) {
	// Normalize drops a leading "the", so these are one venue.
	cs := []Concert{
		{Artist: ArtistRef{Name: "A"}, Date: time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC), Venue: "The Anthem", City: "Washington"},
		{Artist: ArtistRef{Name: "B"}, Date: time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC), Venue: "Anthem", City: "Washington"},
	}
	if got := Apply(cs, Filters{Venue: "Anthem"}); len(got) != 2 {
		t.Errorf("expected 2, got %d", len(got))
	}
}

func TestApply_VenueDoesNotMatchSubstring(t *testing.T) {
	// "Union Stage" must not be pulled in by filtering on "Stage".
	cs := []Concert{
		{Artist: ArtistRef{Name: "A"}, Date: time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC), Venue: "Union Stage", City: "Washington"},
	}
	if got := Apply(cs, Filters{Venue: "Stage"}); len(got) != 0 {
		t.Errorf("venue match must be whole-venue, not substring; got %+v", got)
	}
}

func TestApply_EmptyVenueIsNoFilter(t *testing.T) {
	cs := []Concert{
		{Artist: ArtistRef{Name: "A"}, Date: time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC), Venue: "Union Stage", City: "Washington"},
		{Artist: ArtistRef{Name: "B"}, Date: time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC), Venue: "", City: "Washington"},
	}
	if got := Apply(cs, Filters{Venue: "   "}); len(got) != 2 {
		t.Errorf("blank venue must not filter; got %d", len(got))
	}
}

func TestApply_VenueCombinesWithOtherFilters(t *testing.T) {
	cs := []Concert{
		{Artist: ArtistRef{Name: "A", Genres: []string{"rock"}}, Date: time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC), Venue: "Union Stage", City: "DC"},
		{Artist: ArtistRef{Name: "B", Genres: []string{"jazz"}}, Date: time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC), Venue: "Union Stage", City: "DC"},
	}
	got := Apply(cs, Filters{Venue: "Union Stage", Genre: "jazz"})
	if len(got) != 1 || got[0].Artist.Name != "B" {
		t.Errorf("venue and genre must AND together, got %+v", got)
	}
}
