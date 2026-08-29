package concerts

import (
	"testing"
	"time"
)

func titled(artist, eventName string, festival bool) Concert {
	c := Concert{
		Artist:     ArtistRef{Name: artist},
		Date:       time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
		Venue:      "9:30 Club",
		City:       "Washington",
		EventName:  eventName,
		IsFestival: festival,
	}
	c.DedupKey = DedupKey(c.Artist.Name, c.Date, c.Venue, c.City)
	return c
}

// Ticketmaster names an ordinary show after its performer. Showing that as a
// title would print the artist's name twice on most cards.
func TestEventTitleDroppedWhenItOnlyRepeatsTheAct(t *testing.T) {
	events := GroupEvents([]Concert{titled("Japanese Breakfast", "Japanese Breakfast", false)})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Name != "" {
		t.Errorf("Name = %q, want empty", events[0].Name)
	}
}

func TestEventTitleKeptWhenItAddsSomething(t *testing.T) {
	const tour = "The R&B Tour - Starring Usher Raymond & Chris Brown"
	events := GroupEvents([]Concert{
		titled("USHER", tour, false),
		titled("Chris Brown", tour, false),
	})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (same date, venue, city)", len(events))
	}
	if events[0].Name != tour {
		t.Errorf("Name = %q, want %q", events[0].Name, tour)
	}
}

// One act's source marking the bill a festival is enough: Ticketmaster sets
// the classification on roughly 1 event in 400, so requiring every act to
// agree would throw the signal away nearly every time it shows up.
func TestFestivalMarkerSurvivesOneActCarryingIt(t *testing.T) {
	events := GroupEvents([]Concert{
		titled("Act One", "Some Fest 2026", false),
		titled("Act Two", "Some Fest 2026", true),
	})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if !events[0].IsFestival {
		t.Error("IsFestival = false, want true")
	}
}
