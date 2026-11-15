package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
	"github.com/peterho/concertfinder/internal/testdb"
)

type capturingMail struct {
	mu   sync.Mutex
	msgs []email.Message
}

func (c *capturingMail) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

// The case CF-B26 exists for. A weekly digest in a dense city: 30 genre-only
// shows, all earlier than two shows by artists the user chose (one from the
// Spotify profile, one picked by hand). Date order alone would fill all 25
// places with genre shows and hold the chosen two back week after week.
// Chosen artists lead, soonest first within each group, and only what was
// listed is recorded as sent.
func TestDigestListsChosenArtistsBeforeGenreMatches(t *testing.T) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()

	userID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce, email, digest_opt_in)
VALUES ($1, $2, 'Digest Test', 'ct'::bytea, 'n'::bytea, 'digest@example.com', true)`,
		userID, "sp-"+userID.String()); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.AddArtistPick(ctx, pool, userID, db.ArtistPick{AttractionID: "PICKED", Name: "Picked Band"}); err != nil {
		t.Fatalf("add pick: %v", err)
	}

	fallback := FallbackLocation{Latitude: 40.71, Longitude: -74.0, RadiusMiles: 50}
	base := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 2)
	show := func(id, name string, day int) concerts.Concert {
		d := base.AddDate(0, 0, day).Add(20 * time.Hour)
		local := d.Format("2006-01-02")
		venue := fmt.Sprintf("Room %d", day)
		return concerts.Concert{
			Artist:    concerts.ArtistRef{ID: id, Name: name},
			Date:      d,
			LocalDate: local,
			Venue:     venue,
			City:      "New York",
			Links:     []concerts.TicketLink{{Source: "ticketmaster", URL: "https://tm.example/" + id}},
			DedupKey:  concerts.DedupKey(name, local, venue, "New York"),
		}
	}
	var cs []concerts.Concert
	for i := 0; i < 30; i++ {
		cs = append(cs, show(concerts.TicketmasterArtistID(fmt.Sprintf("GENRE%02d", i)), fmt.Sprintf("Genre Act %02d", i), i))
	}
	cs = append(cs,
		show("spotify-artist-1", "Followed Profile Artist", 40),
		show(concerts.TicketmasterArtistID("PICKED"), "Picked Band", 41),
	)

	rows := make([]db.ConcertRow, len(cs))
	keys := make([]string, len(cs))
	for i, c := range cs {
		body, err := concerts.EncodeSharedRow(c)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = db.ConcertRow{DedupKey: c.DedupKey, Data: body, EventDate: c.Date}
		keys[i] = c.DedupKey
	}
	if err := db.UpsertConcerts(ctx, pool, rows); err != nil {
		t.Fatalf("upsert concerts: %v", err)
	}
	refs, err := json.Marshal(concerts.RefsOf(cs))
	if err != nil {
		t.Fatal(err)
	}
	loc := concerts.Location{Latitude: fallback.Latitude, Longitude: fallback.Longitude, RadiusMiles: fallback.RadiusMiles}
	if err := db.UpsertConcertSnapshot(ctx, pool, db.ConcertSnapshot{
		UserID: userID, LocationKey: LocationKey(loc), DedupKeys: keys,
		ComputedAt: time.Now(), Complete: true, ArtistRefs: refs,
	}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	mail := &capturingMail{}
	w := &SendDigestWorker{
		Pool: pool, Sender: mail, UnsubscribeBase: "https://example.test",
		UnsubscribeToken: func(uuid.UUID) string { return "tok" },
		Fallback:         fallback,
	}
	if err := w.Work(ctx, &river.Job[SendDigestArgs]{JobRow: &rivertype.JobRow{}, Args: SendDigestArgs{UserID: userID}}); err != nil {
		t.Fatalf("digest: %v", err)
	}
	if len(mail.msgs) != 1 {
		t.Fatalf("%d digests sent, want 1", len(mail.msgs))
	}
	text := mail.msgs[0].Text

	profile := strings.Index(text, "Followed Profile Artist")
	picked := strings.Index(text, "Picked Band")
	firstGenre := strings.Index(text, "Genre Act 00")
	if profile < 0 || picked < 0 {
		t.Fatalf("a chosen artist was held back by earlier genre shows:\n%s", text)
	}
	if firstGenre < 0 {
		t.Fatalf("the soonest genre show is missing:\n%s", text)
	}
	mineHead, genreHead := strings.Index(text, "Your artists"), strings.Index(text, "In genres you follow")
	if !(mineHead >= 0 && mineHead < profile && profile < picked && picked < genreHead && genreHead < firstGenre) {
		t.Errorf("order: heading@%d profile@%d picked@%d genre-heading@%d genre@%d, want both chosen first, soonest first, then genre", mineHead, profile, picked, genreHead, firstGenre)
	}
	// 2 chosen + 23 genre fill the 25 places; Genre Act 22 is the last listed.
	if !strings.Contains(text, "Genre Act 22") || strings.Contains(text, "Genre Act 23") {
		t.Errorf("want genre shows 00-22 listed and 23 onward held back:\n%s", text)
	}

	sent, err := db.CountDigestSent(ctx, pool, userID, db.ChannelEmail)
	if err != nil {
		t.Fatal(err)
	}
	if sent != email.DigestMaxEvents {
		t.Errorf("recorded %d as sent, want %d: held-back shows must stay unsent for the next digest", sent, email.DigestMaxEvents)
	}
}
