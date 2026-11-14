package http

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
	"github.com/peterho/concertfinder/internal/jobs"
	"github.com/peterho/concertfinder/internal/testdb"
)

// CF-28's acceptance test: two viewers whose scans produce the same dedup_key
// must each see only their own artist identity.
//
// The `concerts` row is shared by dedup_key and last-writer-wins, and the key
// is artist name + venue day + venue + city -- so a Spotify user and a manual
// user at the same show share one row whatever their profiles say. Until
// CF-28 each scan wrote its artist ID and genres into that row, and whoever
// scanned second decided what both of them saw. Silent in both directions: the
// manual user reads Spotify-derived data, and the Spotify user's bell points at
// a `tm:` ID their subscriptions do not hold, so it renders off and never
// fires.
//
// The manual viewer's results come from the real picks scan (CF-30):
// ScanConcertsWorker over a stub Ticketmaster, with no Spotify client wired in
// at all. The Spotify viewer's results are stated and written through
// jobs.PersistScanResults, the persistence step both scans share, because a
// real profile scan needs Spotify. Every read is the production one: the
// /me/concerts and /me/saved-concerts handlers behind the real session
// middleware, and the digest and instant-notify workers.
//
// Its own database (testdb.Isolated), because the real scan caches tm: rows
// that internal/db's TestScanCachedConcerts would count.
//
// Both orders, because last-writer-wins means a test of one order proves only
// that the second writer sees itself.

type viewer struct {
	label    string
	artistID string
	genres   []string
	spotify  bool

	userID uuid.UUID
	token  string
}

func TestSharedConcertRowLeaksNoArtistIdentityBetweenViewers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		spotifyFirst bool
	}{
		{"spotify scan then manual scan", true},
		{"manual scan then spotify scan", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runCrossViewer(t, tc.spotifyFirst)
		})
	}
}

func runCrossViewer(t *testing.T, spotifyFirst bool) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()

	run := uuid.NewString()[:8]
	sp := &viewer{
		label:    "spotify",
		artistID: "4tZwfgrHOc3mvqYlEYSvVi" + run, // Spotify-shaped
		genres:   []string{"cf28 spotify indie folk " + run},
		spotify:  true,
	}
	mn := &viewer{
		label:    "manual",
		artistID: "tm:K8vZ9171ob7" + run, // Ticketmaster attraction, CF-30's shape
		genres:   []string{"cf28 ticketmaster alternative " + run},
	}
	for _, v := range []*viewer{sp, mn} {
		v.userID, v.token = newFeedUser(t, pool, v)
	}

	loc := concerts.Location{Latitude: 38.9172, Longitude: -77.0369, RadiusMiles: 50}
	for _, v := range []*viewer{sp, mn} {
		if err := db.UpsertUserLocation(ctx, pool, db.UserLocation{
			UserID: v.userID, Latitude: loc.Latitude, Longitude: loc.Longitude, RadiusMiles: loc.RadiusMiles,
		}); err != nil {
			t.Fatalf("set location: %v", err)
		}
	}

	// One show, as each viewer's scan would find it. Same artist name, venue
	// day, venue and city, so the same dedup_key and the same shared row.
	start := time.Now().Add(20 * 24 * time.Hour).UTC().Truncate(time.Hour)
	venue := "CF-28 Test Hall " + run
	show := func(v *viewer) concerts.Concert {
		c := concerts.Concert{
			Artist:    concerts.ArtistRef{ID: v.artistID, Name: "Phoebe Bridgers", Genres: v.genres},
			Date:      start,
			LocalDate: start.Format("2006-01-02"),
			Venue:     venue,
			City:      "Washington",
			State:     "DC",
			Country:   "US",
			Latitude:  loc.Latitude,
			Longitude: loc.Longitude,
			Links:     []concerts.TicketLink{{Source: concerts.SourceTicketmaster, URL: "https://www.ticketmaster.com/event/cf28" + run}},
		}
		c.DedupKey = concerts.DedupKey(c.Artist.Name, c.LocalDay(), c.Venue, c.City)
		return c
	}
	key := show(sp).DedupKey
	if show(mn).DedupKey != key {
		t.Fatal("fixture broken: the two scans must collide on one dedup_key")
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM concerts WHERE dedup_key = $1`, key)
	})

	// The manual viewer picked the artist on Ticketmaster; the stub lists the
	// same show the Spotify viewer's scan found.
	attraction := strings.TrimPrefix(mn.artistID, "tm:")
	if ok, err := db.AddArtistPick(ctx, pool, mn.userID, db.ArtistPick{
		AttractionID: attraction, Name: "Phoebe Bridgers", Genre: mn.genres[0],
	}); err != nil || !ok {
		t.Fatalf("manual pick: ok=%v err=%v", ok, err)
	}
	stub := newTMStub(t, map[string][]tmShow{attraction: {{
		artist: "Phoebe Bridgers", venue: venue, city: "Washington",
		localDate: start.Format("2006-01-02"), lat: loc.Latitude, lng: loc.Longitude,
	}}})
	manualScan := picksScanWorker(pool, stub.client(), nil)

	order := []*viewer{mn, sp}
	if spotifyFirst {
		order = []*viewer{sp, mn}
	}
	for _, v := range order {
		if v == mn {
			runScan(t, manualScan, mn.userID, loc)
			continue
		}
		if err := jobs.PersistScanResults(ctx, pool, v.userID, loc, []concerts.Concert{show(v)}, true, nil); err != nil {
			t.Fatalf("%s scan: persist: %v", v.label, err)
		}
	}
	// The collision is the premise of the whole test, so check the real scan
	// actually produced it rather than assuming the stub lined up.
	if snap, hit, err := db.GetConcertSnapshot(ctx, pool, mn.userID, jobs.LocationKey(loc)); err != nil || !hit ||
		len(snap.DedupKeys) != 1 || snap.DedupKeys[0] != key {
		t.Fatalf("manual scan did not land on the shared dedup_key: hit=%v err=%v keys=%v", hit, err, snap.DedupKeys)
	}

	// The shared row holds what the event source said, and nobody's identity.
	var artist map[string]json.RawMessage
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT data -> 'artist' FROM concerts WHERE dedup_key = $1`, key).Scan(&raw); err != nil {
		t.Fatalf("read shared row: %v", err)
	}
	if err := json.Unmarshal(raw, &artist); err != nil {
		t.Fatalf("decode shared row artist %s: %v", raw, err)
	}
	for _, field := range []string{"id", "genres"} {
		if _, ok := artist[field]; ok {
			t.Errorf("concerts.data.artist carries %q (%s); the shared row must hold no viewer's artist identity", field, raw)
		}
	}

	// Each viewer saves the show and subscribes to the artist as they know it.
	for _, v := range []*viewer{sp, mn} {
		if ok, err := db.SaveConcert(ctx, pool, v.userID, key, 100); err != nil || !ok {
			t.Fatalf("%s save: ok=%v err=%v", v.label, ok, err)
		}
		if ok, err := db.SubscribeArtist(ctx, pool, v.userID, v.artistID, "Phoebe Bridgers", 100); err != nil || !ok {
			t.Fatalf("%s subscribe: ok=%v err=%v", v.label, ok, err)
		}
	}

	r := feedRouter(pool, loc)
	for _, v := range []*viewer{sp, mn} {
		other := mn
		if v == mn {
			other = sp
		}
		for _, path := range []string{"/me/concerts", "/me/saved-concerts"} {
			rec := request(t, r, "GET", path, v.token)
			if rec.Code != 200 {
				t.Fatalf("%s GET %s: %d %s", v.label, path, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			assertNoIdentityOf(t, v.label+" "+path, body, other)
			assertOwnAct(t, v, path, rec.Body.Bytes())
		}
	}

	for _, v := range []*viewer{sp, mn} {
		other := mn
		if v == mn {
			other = sp
		}
		digest := &capturingSender{}
		dw := &jobs.SendDigestWorker{
			Pool:             pool,
			Sender:           digest,
			UnsubscribeBase:  "https://example.test",
			UnsubscribeToken: func(uuid.UUID) string { return "tok" },
			Fallback:         jobs.FallbackLocation{Latitude: loc.Latitude, Longitude: loc.Longitude, RadiusMiles: loc.RadiusMiles},
		}
		if err := dw.Work(ctx, &river.Job[jobs.SendDigestArgs]{JobRow: &rivertype.JobRow{}, Args: jobs.SendDigestArgs{UserID: v.userID}}); err != nil {
			t.Fatalf("%s digest: %v", v.label, err)
		}
		// The digest and instant-notify are two triggers for one email
		// channel and suppress each other through the sent-ledger on
		// purpose. Forget the digest's send so the second worker renders the
		// same show rather than finding it already delivered.
		if _, err := pool.Exec(ctx, `DELETE FROM user_digest_sent WHERE user_id = $1`, v.userID); err != nil {
			t.Fatalf("reset ledger: %v", err)
		}
		notify := &capturingSender{}
		nw := &jobs.SendInstantNotifyWorker{
			Pool:             pool,
			Sender:           notify,
			UnsubscribeBase:  "https://example.test",
			UnsubscribeToken: func(uuid.UUID) string { return "tok" },
		}
		if err := nw.Work(ctx, &river.Job[jobs.SendInstantNotifyArgs]{JobRow: &rivertype.JobRow{}, Args: jobs.SendInstantNotifyArgs{UserID: v.userID, DedupKeys: []string{key}}}); err != nil {
			t.Fatalf("%s instant notify: %v", v.label, err)
		}
		for name, s := range map[string]*capturingSender{"digest": digest, "instant-notify": notify} {
			msgs := s.all()
			// The positive control: a mail that was never rendered would pass
			// every "does not contain" check below.
			if len(msgs) != 1 {
				t.Fatalf("%s %s: %d messages sent, want 1", v.label, name, len(msgs))
			}
			text := msgs[0].Subject + "\n" + msgs[0].Text + "\n" + msgs[0].HTML
			if !strings.Contains(text, "Phoebe Bridgers") {
				t.Errorf("%s %s does not name the show's artist; the check below proves nothing", v.label, name)
			}
			assertNoIdentityOf(t, v.label+" "+name, text, other)
		}
	}
}

// assertNoIdentityOf fails when text carries any of other's artist identity.
func assertNoIdentityOf(t *testing.T, where, text string, other *viewer) {
	t.Helper()
	if strings.Contains(text, other.artistID) {
		t.Errorf("%s carries the %s viewer's artist ID %q", where, other.label, other.artistID)
	}
	for _, g := range other.genres {
		if strings.Contains(text, g) {
			t.Errorf("%s carries the %s viewer's genre %q", where, other.label, g)
		}
	}
}

// assertOwnAct checks the viewer's card still works for them: their own ID,
// their own genres, the bell lit for the artist they subscribed to, and a
// genre facet that is theirs. Without it the leak could be "fixed" by
// serving nobody any identity at all, which is the opposite failure.
func assertOwnAct(t *testing.T, v *viewer, path string, body []byte) {
	t.Helper()
	var resp struct {
		Events []concerts.Event `json:"events"`
		Facets facetSet         `json:"facets"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(resp.Events) != 1 || len(resp.Events[0].Acts) != 1 {
		t.Fatalf("%s %s: want one event with one act, got %s", v.label, path, body)
	}
	act := resp.Events[0].Acts[0]
	if act.Artist.ID != v.artistID {
		t.Errorf("%s %s: artist id = %q, want their own %q", v.label, path, act.Artist.ID, v.artistID)
	}
	if strings.Join(act.Artist.Genres, ",") != strings.Join(v.genres, ",") {
		t.Errorf("%s %s: genres = %v, want their own %v", v.label, path, act.Artist.Genres, v.genres)
	}
	if !act.Subscribed {
		t.Errorf("%s %s: bell is off for the artist they subscribed to", v.label, path)
	}
	if !act.Saved {
		t.Errorf("%s %s: star is off for the show they saved", v.label, path)
	}
	if len(resp.Facets.Genres) != 1 || resp.Facets.Genres[0].Value != v.genres[0] {
		t.Errorf("%s %s: genre facets = %+v, want only %q", v.label, path, resp.Facets.Genres, v.genres[0])
	}
}

// newFeedUser creates an opted-in account with a live session. The manual
// viewer has no Spotify ID at all (migration 0023 made it nullable) and an
// email identity instead, as CF-33's users will.
func newFeedUser(t *testing.T, pool *pgxpool.Pool, v *viewer) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	var spotifyID any
	if v.spotify {
		spotifyID = "spotify-cf28-" + id.String()
	}
	const insert = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce,
                   email, digest_opt_in, instant_notify_opt_in)
VALUES ($1, $2, $3, $4, $5, $6, true, true)`
	if _, err := pool.Exec(ctx, insert, id, spotifyID, "CF-28 "+v.label,
		[]byte("ciphertext"), []byte("nonce-123456"), "cf28-"+id.String()+"@example.com"); err != nil {
		t.Fatalf("insert %s user: %v", v.label, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	provider, subject := "email", "cf28-"+id.String()+"@example.com"
	if v.spotify {
		provider, subject = "spotify", spotifyID.(string)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_identities (provider, subject, user_id) VALUES ($1, $2, $3)`,
		provider, subject, id); err != nil {
		t.Fatalf("insert %s identity: %v", v.label, err)
	}
	token := uuid.NewString() + uuid.NewString()
	if err := db.CreateSession(ctx, pool, db.Session{
		ID:        uuid.NewString(),
		TokenHash: auth.HashSessionToken(token),
		UserID:    id,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id, token
}

// feedRouter mounts the two feed endpoints the way main.go does, behind the
// real session middleware. River is nil: the snapshots are fresh and
// complete, so nothing would be enqueued anyway.
func feedRouter(pool *pgxpool.Pool, loc concerts.Location) chi.Router {
	r := chi.NewRouter()
	concertsH := &ConcertsHandler{Pool: pool, FallbackLocation: loc, SnapshotStaleAfter: time.Hour}
	savedH := &SavedConcertsHandler{Pool: pool, FallbackLocation: loc}
	r.Route("/me", func(r chi.Router) {
		r.Use(auth.RequireUser(pool))
		r.Get("/concerts", concertsH.Get)
		r.Get("/saved-concerts", savedH.List)
	})
	return r
}

// capturingSender records what a worker rendered instead of sending it.
type capturingSender struct {
	mu   sync.Mutex
	msgs []email.Message
}

func (s *capturingSender) Send(_ context.Context, m email.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	return nil
}

func (s *capturingSender) all() []email.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]email.Message(nil), s.msgs...)
}
