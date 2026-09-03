package concerts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/ticketmaster"
)

var (
	discoverFloor = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	showTime      = time.Date(2026, 10, 3, 19, 30, 0, 0, time.UTC)
)

func tmBlob(t *testing.T, evs ...ticketmaster.Event) []byte {
	t.Helper()
	b, err := json.Marshal(evs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func anthem(lineup ...string) ticketmaster.Event {
	e := ticketmaster.Event{
		ID:    "Z7r9jZ1A7aVeb",
		Name:  "Fontaines D.C.",
		URL:   "https://www.ticketmaster.com/event/anthem",
		Start: showTime,
		Venue: ticketmaster.Venue{
			Name: "The Anthem", City: "Washington", State: "DC", Country: "US",
			Latitude: 38.8790, Longitude: -77.0217,
		},
	}
	for i, name := range lineup {
		e.Lineup = append(e.Lineup, ticketmaster.Attraction{ID: string(rune('A' + i)), Name: name})
	}
	return e
}

// The same show is cached once per artist whose scan found it, so the raw
// input to this view is full of duplicates. They have to collapse the same
// way the signed-in feed's do, or the login page shows one gig four times.
func TestFromCachedTicketmasterDedupesAcrossPayloads(t *testing.T) {
	blobs := [][]byte{
		tmBlob(t, anthem("Fontaines D.C.", "Been Stellar")),
		tmBlob(t, anthem("Fontaines D.C.", "Been Stellar")),
	}
	got := FromCachedTicketmaster(blobs, discoverFloor)
	if len(got) != 2 {
		t.Fatalf("want one concert per act, got %d: %+v", len(got), got)
	}
	events := GroupEvents(got)
	if len(events) != 1 || len(events[0].Acts) != 2 {
		t.Fatalf("want one event with two acts, got %+v", events)
	}
	if n := len(events[0].Links); n != 1 {
		t.Errorf("want the duplicate ticket link merged away, got %d", n)
	}
}

func TestFromCachedTicketmasterCapsTheBill(t *testing.T) {
	names := []string{"A", "B", "C", "D", "E", "F", "G"}
	got := FromCachedTicketmaster([][]byte{tmBlob(t, anthem(names...))}, discoverFloor)
	if len(got) != DiscoverMaxActs {
		t.Fatalf("want the bill capped at %d acts, got %d", DiscoverMaxActs, len(got))
	}
}

// Ticketmaster titles an ordinary club show after its performer, so an event
// with no lineup array still names an artist worth showing.
func TestFromCachedTicketmasterFallsBackToTheEventName(t *testing.T) {
	got := FromCachedTicketmaster([][]byte{tmBlob(t, anthem())}, discoverFloor)
	if len(got) != 1 || got[0].Artist.Name != "Fontaines D.C." {
		t.Fatalf("want one act named after the event, got %+v", got)
	}
}

func TestFromCachedTicketmasterSkipsWhatItCannotPlace(t *testing.T) {
	noCoords := anthem("Somebody")
	noCoords.Venue.Latitude, noCoords.Venue.Longitude = 0, 0
	past := anthem("Yesterday")
	past.Start = discoverFloor.Add(-time.Hour)

	got := FromCachedTicketmaster([][]byte{tmBlob(t, noCoords, past)}, discoverFloor)
	if len(got) != 0 {
		t.Fatalf("want nothing: an unplaceable venue and a past show, got %+v", got)
	}
}

// A payload written by an older release must not fail the request. These rows
// outlive the code that wrote them by up to a week.
func TestFromCachedTicketmasterSkipsUndecodablePayloads(t *testing.T) {
	got := FromCachedTicketmaster([][]byte{[]byte(`{"not":"an array"}`), tmBlob(t, anthem("Real"))}, discoverFloor)
	if len(got) != 1 || got[0].Artist.Name != "Real" {
		t.Fatalf("want the decodable payload to survive alone, got %+v", got)
	}
}

func TestNearFiltersByRadius(t *testing.T) {
	cs := FromCachedTicketmaster([][]byte{tmBlob(t, anthem("Fontaines D.C."))}, discoverFloor)
	dc := Location{Latitude: 38.8951, Longitude: -77.0364, RadiusMiles: 25}
	if got := Near(cs, dc); len(got) != 1 {
		t.Errorf("want the DC show for a DC visitor, got %d", len(got))
	}
	chicago := Location{Latitude: 41.8781, Longitude: -87.6298, RadiusMiles: 25}
	if got := Near(cs, chicago); len(got) != 0 {
		t.Errorf("want nothing for a Chicago visitor, got %d", len(got))
	}
}
