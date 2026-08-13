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
	// Two genuinely separate shows — same artist twice at the same room on
	// the same night is one event, which is the whole point of the grouping.
	two := []concerts.Concert{
		one[0],
		{Artist: concerts.ArtistRef{Name: "Y"}, Date: when.AddDate(0, 0, 1), Venue: "V2", City: "C"},
	}
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

// festival returns n acts sharing one night at one room — the shape the
// grouping exists for. Set times differ, as they do at a real festival.
func festival(names ...string) []concerts.Concert {
	day := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	cs := make([]concerts.Concert, 0, len(names))
	for i, n := range names {
		cs = append(cs, concerts.Concert{
			Artist: concerts.ArtistRef{Name: n},
			Date:   day.Add(time.Duration(14+i) * time.Hour),
			Venue:  "Merriweather Post Pavilion",
			City:   "Columbia",
			State:  "MD",
			Links:  []concerts.TicketLink{{Source: concerts.SourceTicketmaster, URL: "https://tm.example/fest"}},
		})
	}
	return cs
}

// The bug: one night out mailed as six rows under a subject counting six.
// The digest's net-new set is per (artist, show), so a festival arrives as one
// Concert per matched artist and the renderer has to fold them.
func TestRenderDigestCountsEventsNotConcerts(t *testing.T) {
	m := RenderDigest("peter", "p@ex", festival("A", "B", "C", "D", "E", "F"), "https://ex/u")
	if !strings.Contains(m.Subject, "1 new show") || strings.Contains(m.Subject, "shows") {
		t.Errorf("six acts on one bill must count as one show; got %q", m.Subject)
	}
	if n := strings.Count(m.Text, "Merriweather Post Pavilion"); n != 1 {
		t.Errorf("venue should appear once, appeared %d times\n%s", n, m.Text)
	}
	if n := strings.Count(m.HTML, "<li "); n != 1 {
		t.Errorf("expected one list item, got %d\n%s", n, m.HTML)
	}
	// Every act still has to be named somewhere, and the shared ticket link
	// must not be repeated once per act.
	for _, name := range []string{"A", "B", "C", "D"} {
		if !strings.Contains(m.Text, name) {
			t.Errorf("act %q missing from text body\n%s", name, m.Text)
		}
	}
	if n := strings.Count(m.Text, "https://tm.example/fest"); n != 1 {
		t.Errorf("shared ticket link should be unioned to one, got %d\n%s", n, m.Text)
	}
}

func TestActsLinePhrasing(t *testing.T) {
	act := func(names ...string) []concerts.Act {
		out := make([]concerts.Act, 0, len(names))
		for _, n := range names {
			out = append(out, concerts.Act{Artist: concerts.ArtistRef{Name: n}})
		}
		return out
	}
	cases := []struct {
		in   []concerts.Act
		want string
	}{
		{act(), ""},
		{act("A"), "A"},
		{act("A", "B"), "A and B"},
		{act("A", "B", "C"), "A, B and C"},
		{act("A", "B", "C", "D"), "A, B, C and D"},
		// Past the cap the tail is summarized rather than dumped — a big
		// festival can match dozens of profile artists.
		{act("A", "B", "C", "D", "E"), "A, B, C, D and 1 more"},
		{act("A", "B", "C", "D", "E", "F"), "A, B, C, D and 2 more"},
	}
	for _, c := range cases {
		if got := actsLine(c.in); got != c.want {
			t.Errorf("actsLine(%d acts) = %q, want %q", len(c.in), got, c.want)
		}
	}
}

// Instant-notify is per-subscription, so its input is already narrowed to
// artists the user subscribed to. Folding a bill can therefore only ever name
// artists they asked about — and it stops three subscriptions colliding at one
// festival from mailing "3 new shows" for one night.
func TestRenderInstantNotifyGroupsBill(t *testing.T) {
	m := RenderInstantNotify("peter", "p@ex", festival("Beach House", "Slowdive"), "https://ex/u")
	if strings.Contains(m.Subject, "2 new shows") {
		t.Errorf("two acts on one bill is one show; got %q", m.Subject)
	}
	for _, name := range []string{"Beach House", "Slowdive"} {
		if !strings.Contains(m.Subject, name) {
			t.Errorf("subject should name both subscribed acts; got %q", m.Subject)
		}
	}
	// "an artist you're following" would be wrong for a two-act bill.
	if strings.Contains(m.Text, "an artist you're following") {
		t.Errorf("multi-act intro should read plural\n%s", m.Text)
	}
	if !strings.Contains(m.Text, "artists you're following") {
		t.Errorf("multi-act intro missing plural phrasing\n%s", m.Text)
	}
}

func TestRenderInstantNotifySingleActKeepsSingularVoice(t *testing.T) {
	m := RenderInstantNotify("peter", "p@ex", festival("Slowdive"), "https://ex/u")
	if !strings.Contains(m.Subject, "New show: Slowdive") {
		t.Errorf("subject wrong: %q", m.Subject)
	}
	if !strings.Contains(m.Text, "an artist you're following") {
		t.Errorf("single-act intro should read singular\n%s", m.Text)
	}
}

// Separate nights stay separate — grouping keys on (date, venue, city), so a
// residency must not collapse into one entry.
func TestRenderInstantNotifyKeepsDistinctNightsApart(t *testing.T) {
	day := time.Date(2026, 7, 4, 20, 0, 0, 0, time.UTC)
	cs := []concerts.Concert{
		{Artist: concerts.ArtistRef{Name: "Slowdive"}, Date: day, Venue: "9:30 Club", City: "Washington"},
		{Artist: concerts.ArtistRef{Name: "Slowdive"}, Date: day.AddDate(0, 0, 1), Venue: "9:30 Club", City: "Washington"},
	}
	m := RenderInstantNotify("peter", "p@ex", cs, "https://ex/u")
	if !strings.Contains(m.Subject, "2 new shows") {
		t.Errorf("two nights at one room are two shows; got %q", m.Subject)
	}
}
