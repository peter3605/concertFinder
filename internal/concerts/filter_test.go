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

func TestApply_GenreSubstringCaseInsensitive(t *testing.T) {
	cs := []Concert{mk(1, []string{"post-rock"}), mk(2, []string{"jazz"}), mk(3, nil)}
	got := Apply(cs, Filters{Genre: "ROCK"})
	if len(got) != 1 || got[0].Artist.Genres[0] != "post-rock" {
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
