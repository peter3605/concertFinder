package concerts

import (
	"testing"
	"time"
)

func act(name string, hour int, venue string) Concert {
	d := time.Date(2026, 7, 18, hour, 0, 0, 0, time.UTC)
	return Concert{
		Artist:   ArtistRef{ID: name, Name: name},
		Date:     d,
		Venue:    venue,
		City:     "Chicago",
		DedupKey: DedupKey(name, d, venue, "Chicago"),
	}
}

// The whole point: a festival where the user matched six artists is one
// thing they can attend, not six rows.
func TestGroupEventsMergesOneBill(t *testing.T) {
	cs := []Concert{
		act("Alvvays", 14, "Union Park"),
		act("Jamie xx", 20, "Union Park"),
		act("Yaeji", 17, "Union Park"),
	}
	got := GroupEvents(cs)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if len(got[0].Acts) != 3 {
		t.Fatalf("expected 3 acts, got %d", len(got[0].Acts))
	}
	// Set times differ within a festival; the card sorts by the earliest.
	if got[0].Date.Hour() != 14 {
		t.Errorf("event date should be the earliest set time, got %v", got[0].Date)
	}
}

// Different set times must NOT split a bill — the reason EventKey is
// day-granular rather than using the full timestamp.
func TestGroupEventsIgnoresSetTimes(t *testing.T) {
	if EventKey(act("A", 14, "Union Park").Date, "Union Park", "Chicago") !=
		EventKey(act("B", 22, "Union Park").Date, "Union Park", "Chicago") {
		t.Error("acts on the same day at the same venue must share an event key")
	}
}

func TestGroupEventsKeepsDistinctShowsApart(t *testing.T) {
	cs := []Concert{
		act("A", 20, "Union Park"),
		act("B", 20, "Thalia Hall"),
	}
	if got := GroupEvents(cs); len(got) != 2 {
		t.Fatalf("different venues are different events, got %d", len(got))
	}
}

// Venue spellings that dedup treats as one room have to group as one event
// too, or the same festival splits by whichever feed found each artist.
func TestGroupEventsNormalizesVenue(t *testing.T) {
	cs := []Concert{act("A", 20, "9:30 CLUB"), act("B", 20, "9:30 Club")}
	if got := GroupEvents(cs); len(got) != 1 {
		t.Errorf("one room spelled two ways is one event, got %d", len(got))
	}
}

// Sources routinely hand every artist on a bill the same festival URL.
func TestGroupEventsUnionsLinksWithoutDuplicating(t *testing.T) {
	a := act("A", 20, "Union Park")
	a.Links = []TicketLink{{Source: SourceTicketmaster, URL: "https://tm/fest"}}
	b := act("B", 21, "Union Park")
	b.Links = []TicketLink{
		{Source: SourceTicketmaster, URL: "https://tm/fest"},
		{Source: SourceBandsintown, URL: "https://bit/fest?app_id=x"},
	}
	got := GroupEvents([]Concert{a, b})
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if len(got[0].Links) != 2 {
		t.Fatalf("expected 2 deduped links, got %+v", got[0].Links)
	}
	// Source priority still governs which link leads.
	if got[0].Links[0].Source != SourceTicketmaster {
		t.Errorf("links should stay in priority order, got %+v", got[0].Links)
	}
}

// Saves and subscriptions stay per artist, so each act carries its own
// flags onto the shared card.
func TestGroupEventsPreservesPerActFlags(t *testing.T) {
	a := act("A", 20, "Union Park")
	a.Saved = true
	b := act("B", 21, "Union Park")
	b.Subscribed = true
	got := GroupEvents([]Concert{a, b})
	if len(got) != 1 || len(got[0].Acts) != 2 {
		t.Fatalf("expected 1 event with 2 acts, got %+v", got)
	}
	if !got[0].Acts[0].Saved || got[0].Acts[0].Subscribed {
		t.Errorf("act A should be saved and not subscribed, got %+v", got[0].Acts[0])
	}
	if got[0].Acts[1].Saved || !got[0].Acts[1].Subscribed {
		t.Errorf("act B should be subscribed and not saved, got %+v", got[0].Acts[1])
	}
	// The act keeps its own dedup_key — that's what save/unsave posts back.
	if got[0].Acts[0].DedupKey != a.DedupKey {
		t.Error("act must carry its own dedup key for save round-trips")
	}
}

// Snapshots arrive date-ordered; grouping must not shuffle them through a
// map.
func TestGroupEventsPreservesInputOrder(t *testing.T) {
	mk := func(name string, day int) Concert {
		d := time.Date(2026, 7, day, 20, 0, 0, 0, time.UTC)
		return Concert{Artist: ArtistRef{Name: name}, Date: d, Venue: "V" + name, City: "Chicago"}
	}
	cs := []Concert{mk("A", 1), mk("B", 2), mk("C", 3)}
	for i := 0; i < 50; i++ { // map iteration order varies run to run
		got := GroupEvents(cs)
		if len(got) != 3 || got[0].Venue != "VA" || got[2].Venue != "VC" {
			t.Fatalf("order not preserved: %+v", got)
		}
	}
}

func TestGroupEventsEmpty(t *testing.T) {
	if got := GroupEvents(nil); len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}

func TestCountEventKeys(t *testing.T) {
	cs := []Concert{
		act("A", 14, "Union Park"),
		act("B", 20, "Union Park"),
		act("C", 20, "Thalia Hall"),
	}
	if got := CountEventKeys(cs); got != 2 {
		t.Errorf("expected 2 distinct events, got %d", got)
	}
}

// Reason rides onto the Act with Saved and Subscribed. It is applied to the
// Concert rows by the handler and only ever read off the Act by a client, so
// dropping it here would blank the line on every card with nothing failing.
func TestGroupEventsCarriesPerActReason(t *testing.T) {
	when := time.Date(2026, 10, 3, 19, 30, 0, 0, time.UTC)
	got := GroupEvents([]Concert{
		{Artist: ArtistRef{ID: "a", Name: "Alpha"}, Date: when, Venue: "The Anthem", City: "Washington", Reason: "You follow them"},
		{Artist: ArtistRef{ID: "b", Name: "Beta"}, Date: when, Venue: "The Anthem", City: "Washington"},
	})
	if len(got) != 1 || len(got[0].Acts) != 2 {
		t.Fatalf("want one event with two acts, got %+v", got)
	}
	if got[0].Acts[0].Reason != "You follow them" {
		t.Errorf("first act lost its reason: %q", got[0].Acts[0].Reason)
	}
	if got[0].Acts[1].Reason != "" {
		t.Errorf("second act invented a reason: %q", got[0].Acts[1].Reason)
	}
}
