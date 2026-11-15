package email

import (
	"context"
	"flag"
	"fmt"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
)

var update = flag.Bool("update", false, "rewrite the committed previews under testdata")

// previewConcerts is the fixture the committed preview is rendered from. It
// carries every shape the layout has to handle: shows across several months,
// a festival matching several acts, an event with two Ticketmaster URLs and a
// second source, reason lines of both kinds, and genre matches for the second
// section. The caller caps it, so the preview also shows "...and N more".
func previewConcerts() []concerts.Concert {
	tm := func(id string) concerts.TicketLink {
		return concerts.TicketLink{Source: concerts.SourceTicketmaster, URL: "https://www.ticketmaster.com/event/" + id}
	}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 20, 0, 0, 0, time.UTC) }
	c := func(id, name string, when time.Time, venue, city, state string, links ...concerts.TicketLink) concerts.Concert {
		cc := concerts.Concert{Artist: concerts.ArtistRef{ID: id, Name: name}, Date: when, Venue: venue, City: city, State: state, Links: links}
		cc.DedupKey = concerts.DedupKey(name, cc.LocalDay(), venue, city)
		return cc
	}
	cs := []concerts.Concert{
		c("sp-doja", "Doja Cat", day(11, 20), "Madison Square Garden", "New York", "NY", tm("1")),
		c("sp-common", "Common People", day(11, 17), "Bowery Ballroom", "New York", "NY", tm("2a"), tm("2b"),
			concerts.TicketLink{Source: concerts.SourceSongkick, URL: "https://www.songkick.com/concerts/2"}),
		c("tm:K8vZ917", "Wet Leg", day(12, 5), "Brooklyn Steel", "Brooklyn", "NY", tm("3")),
		c("sp-fontaines", "Fontaines D.C.", day(1, 15).AddDate(1, 0, 0), "Terminal 5", "New York", "NY", tm("4")),
	}
	// A festival night matching three of the user's artists.
	for i, name := range []string{"Japanese Breakfast", "Alvvays", "Snail Mail"} {
		f := c("sp-fest"+fmt.Sprint(i), name, day(12, 12).Add(time.Duration(i)*time.Hour), "Forest Hills Stadium", "Queens", "NY", tm("fest"))
		f.EventName = "Winter Lights Festival"
		f.IsFestival = true
		cs = append(cs, f)
	}
	// Genre matches.
	for i := 0; i < 24; i++ {
		cs = append(cs, c(fmt.Sprintf("tm:G%d", i), fmt.Sprintf("Genre Act %d", i+1),
			day(11, 18).AddDate(0, 0, i*3), fmt.Sprintf("Room %d", i+1), "New York", "NY", tm(fmt.Sprintf("g%d", i))))
	}
	return cs
}

func previewMatches() concerts.ViewerMatches {
	return concerts.ViewerMatches{
		Picked: map[string]struct{}{"tm:K8vZ917": {}},
		Reasons: map[string]string{
			"sp-doja":      "#3 in your top artists",
			"sp-common":    "You follow them",
			"sp-fest1":     "On your saved albums",
			"sp-fontaines": "#12 in your top artists",
		},
	}
}

// renderPreview runs the fixture through the same steps SendDigestWorker
// does: chosen first, tag, cap, render.
func renderPreview(a Audience) Message {
	m := previewMatches()
	cs := concerts.ChosenFirst(previewConcerts(), m.Chosen)
	m.Tag(cs)
	kept, more := CapDigest(cs, DigestMaxEvents)
	return RenderDigest(a, kept, more, m.Chosen)
}

var previewAudience = Audience{
	Email:          "listener@example.com",
	Spotify:        true,
	SiteURL:        "https://concertfinder.app",
	UnsubscribeURL: "https://concertfinder.app/api/unsubscribe?token=preview",
}

// TestDigestPreview keeps internal/email/testdata/digest_preview.html in
// step with the renderer. Open that file in a browser to see the email.
// Regenerate with: go test ./internal/email -run Preview -update
func TestDigestPreview(t *testing.T) {
	m := renderPreview(previewAudience)
	golden(t, "digest_preview.html", m.HTML)
	golden(t, "digest_preview.txt", "Subject: "+m.Subject+"\n\n"+m.Text)

	inst := previewConcerts()[:1]
	previewMatches().Tag(inst)
	im := RenderInstantNotify(previewAudience, inst)
	golden(t, "instant_preview.html", im.HTML)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (regenerate with -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s is out of date with the renderer; regenerate with: go test ./internal/email -run Preview -update", path)
	}
}

// Gmail clips a message past about 102KB and hides the rest behind a link,
// which would take the footer -- and the unsubscribe -- with it.
func TestFullDigestStaysUnderGmailClip(t *testing.T) {
	m := renderPreview(previewAudience)
	if n := len(m.HTML); n > 60_000 {
		t.Errorf("a full digest's HTML is %d bytes; keep it well under Gmail's ~102KB clip", n)
	}
}

func TestPreviewFixtureShowsEveryShape(t *testing.T) {
	m := renderPreview(previewAudience)
	for _, want := range []string{
		"Your artists", "In genres you follow", // both sections
		"November 2026", "December 2026", "January 2027", // several months
		"Japanese Breakfast, Alvvays and Snail Mail", "Winter Lights Festival", // the festival, once
		"You picked them", "#3 in your top artists", "Alvvays: On your saved albums", // reasons
		"more.", // capped
	} {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("preview HTML is missing %q", want)
		}
	}
	if !strings.HasPrefix(m.Subject, "Common People, Doja Cat and ") {
		t.Errorf("subject should lead with the user's own artists, soonest first; got %q", m.Subject)
	}
}

func TestSpotifyAttributionOnlyForSpotifyUsers(t *testing.T) {
	cs := festival("Slowdive")
	for _, spotify := range []bool{true, false} {
		a := aud
		a.Spotify = spotify
		for name, m := range map[string]Message{
			"digest":  RenderDigest(a, cs, 0, nil),
			"instant": RenderInstantNotify(a, cs),
		} {
			for part, body := range map[string]string{"html": m.HTML, "text": m.Text} {
				if got := strings.Contains(body, "Powered by Spotify"); got != spotify {
					t.Errorf("%s %s, spotify=%v: attribution present=%v", name, part, spotify, got)
				}
			}
		}
	}
}

// The 2026-11-14 digest showed "ticketmaster ticketmaster" under one show:
// two distinct Ticketmaster URLs, each printed under its source slug.
func TestOneTicketButtonAndExtraSourcesByName(t *testing.T) {
	when := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	cs := []concerts.Concert{{
		Artist: concerts.ArtistRef{Name: "Common People"}, Date: when, Venue: "Bowery Ballroom", City: "New York",
		Links: []concerts.TicketLink{
			{Source: concerts.SourceTicketmaster, URL: "https://tm.example/a"},
			{Source: concerts.SourceTicketmaster, URL: "https://tm.example/b"},
			{Source: concerts.SourceSongkick, URL: "https://sk.example/c"},
		},
	}}
	m := RenderDigest(aud, cs, 0, nil)
	if n := strings.Count(m.HTML, ">Get tickets</a>"); n != 1 {
		t.Errorf("want one Get tickets button, got %d", n)
	}
	if strings.Contains(m.HTML, "https://tm.example/b") || strings.Contains(m.Text, "https://tm.example/b") {
		t.Error("a second link from the same source should not be repeated")
	}
	if !strings.Contains(m.HTML, `>Songkick</a>`) || !strings.Contains(m.Text, "Also on Songkick: https://sk.example/c") {
		t.Errorf("the extra source should be labelled by name\n%s", m.Text)
	}
	if strings.Contains(strings.ToLower(m.Text), "ticketmaster ticketmaster") || strings.Contains(m.HTML, ">ticketmaster<") {
		t.Error("source slugs leaked into the body")
	}
}

func TestReasonLineRendersWhenPresent(t *testing.T) {
	cs := festival("Slowdive")
	cs[0].Reason = "You picked them"
	m := RenderDigest(aud, cs, 0, nil)
	if !strings.Contains(m.HTML, "You picked them") || !strings.Contains(m.Text, "You picked them") {
		t.Error("reason line missing")
	}
	bare := RenderDigest(aud, festival("Slowdive"), 0, nil)
	if strings.Contains(bare.HTML, `class="rs"`) {
		t.Error("an act with no reason should get no reason line")
	}
}

func TestLayoutChrome(t *testing.T) {
	for name, m := range map[string]Message{
		"digest":  RenderDigest(aud, festival("Slowdive"), 0, nil),
		"instant": RenderInstantNotify(aud, festival("Slowdive")),
	} {
		for _, want := range []string{
			`<img src="https://site.example/icon-512.png"`, `alt="ConcertFinder"`,
			`>See all in ConcertFinder</a>`,
			`href="https://site.example/settings"`, `>Manage email preferences</a>`,
			`href="https://ex/u"`, `>Unsubscribe</a>`,
			`max-width:600px`,
			`display:none;max-height:0`, // the preheader
		} {
			if !strings.Contains(m.HTML, want) {
				t.Errorf("%s HTML missing %q", name, want)
			}
		}
		for _, want := range []string{"See all in ConcertFinder: https://site.example/", "Manage email preferences: https://site.example/settings", "Unsubscribe: https://ex/u"} {
			if !strings.Contains(m.Text, want) {
				t.Errorf("%s text missing %q", name, want)
			}
		}
		// No greeting: "Hi peter.ho433" read the account handle back to
		// people as though it were their name.
		if strings.Contains(m.Text, "Hi ") || strings.Contains(m.HTML, "Hi ") {
			t.Errorf("%s still greets by handle", name)
		}
	}
}

func TestPreheaderNamesLeadingActs(t *testing.T) {
	when := time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)
	var cs []concerts.Concert
	for i, n := range []string{"A1", "B2", "C3", "D4", "E5", "F6"} {
		cs = append(cs, concerts.Concert{Artist: concerts.ArtistRef{Name: n}, Date: when.AddDate(0, 0, i), Venue: n + " room", City: "C"})
	}
	m := RenderDigest(aud, cs, 0, nil)
	if !strings.Contains(m.HTML, ">A1, B2, C3, D4 and 2 more") {
		t.Errorf("preheader should name the first four acts and count the rest\n%s", m.HTML[:800])
	}
}

// The name goes in the header and only the address goes in MAIL FROM.
// smtp.SendMail given a named From sends `MAIL FROM:<ConcertFinder <...>>`,
// which SES rejects.
func TestSenderNameInHeaderBareAddressInEnvelope(t *testing.T) {
	for _, from := range []string{"notifications@concertfinder.app", "ConcertFinder <notifications@concertfinder.app>"} {
		var envelope string
		var raw []byte
		s := &Sender{
			Cfg: Config{Mode: ModeSMTP, Host: "smtp.example", Port: 587, From: from},
			sendMail: func(_ string, _ smtp.Auth, f string, _ []string, msg []byte) error {
				envelope, raw = f, msg
				return nil
			},
		}
		if err := s.Send(context.Background(), RenderSignIn("p@ex", "https://ex/l", "123456", SignInLogin)); err != nil {
			t.Fatalf("from %q: %v", from, err)
		}
		if envelope != "notifications@concertfinder.app" {
			t.Errorf("from %q: envelope sender = %q, want the bare address", from, envelope)
		}
		if got := parseHeaders(t, raw).Get("From"); got != `"ConcertFinder" <notifications@concertfinder.app>` {
			t.Errorf("from %q: From header = %q", from, got)
		}
	}
}

func TestSenderRefusesAnUnparseableFrom(t *testing.T) {
	s := &Sender{Cfg: Config{Mode: ModeSMTP, From: "not an address"},
		sendMail: func(string, smtp.Auth, string, []string, []byte) error {
			t.Fatal("should not reach the relay")
			return nil
		}}
	if err := s.Send(context.Background(), Message{To: "p@ex"}); err == nil {
		t.Error("want an error for an unparseable SMTP_FROM")
	}
}
