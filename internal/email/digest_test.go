package email

import (
	"strings"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
)

func TestRenderDigestSubjectPluralization(t *testing.T) {
	when := time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)
	one := []concerts.Concert{{Artist: concerts.ArtistRef{Name: "X"}, Date: when, Venue: "V", City: "C"}}
	two := append([]concerts.Concert{}, one[0], one[0])
	if got := RenderDigest("peter", "p@ex", one, "https://ex/unsub").Subject; !strings.Contains(got, "1 new show") || strings.Contains(got, "shows") {
		t.Errorf("singular subject wrong: %q", got)
	}
	if got := RenderDigest("peter", "p@ex", two, "https://ex/unsub").Subject; !strings.Contains(got, "2 new shows") {
		t.Errorf("plural subject wrong: %q", got)
	}
}

func TestRenderDigestIncludesArtistVenueUnsub(t *testing.T) {
	when := time.Date(2026, 10, 3, 23, 0, 0, 0, time.UTC)
	cs := []concerts.Concert{
		{Artist: concerts.ArtistRef{Name: "Olivia Rodrigo"}, Date: when, Venue: "Capital One Arena", City: "Washington", State: "DC",
			Links: []concerts.TicketLink{{Source: concerts.SourceTicketmaster, URL: "https://tm.example/1"}}},
	}
	m := RenderDigest("peter", "p@ex", cs, "https://site.example/api/unsubscribe?token=abc")
	for _, needle := range []string{"Olivia Rodrigo", "Capital One Arena", "Washington", "https://tm.example/1", "https://site.example/api/unsubscribe?token=abc"} {
		if !strings.Contains(m.Text, needle) {
			t.Errorf("text body missing %q\n%s", needle, m.Text)
		}
		if !strings.Contains(m.HTML, needle) {
			t.Errorf("html body missing %q", needle)
		}
	}
}

func TestRenderDigestGroupsByMonth(t *testing.T) {
	cs := []concerts.Concert{
		{Artist: concerts.ArtistRef{Name: "A"}, Date: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), Venue: "V1", City: "C"},
		{Artist: concerts.ArtistRef{Name: "B"}, Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Venue: "V2", City: "C"},
		{Artist: concerts.ArtistRef{Name: "C"}, Date: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), Venue: "V3", City: "C"},
	}
	m := RenderDigest("peter", "p@ex", cs, "https://ex/u")
	// August entries appear before September header — check ordering by
	// scanning positions.
	aug := strings.Index(m.Text, "August 2026")
	sep := strings.Index(m.Text, "September 2026")
	if aug < 0 || sep < 0 || aug > sep {
		t.Errorf("month grouping/order wrong; aug=%d sep=%d\n%s", aug, sep, m.Text)
	}
}
