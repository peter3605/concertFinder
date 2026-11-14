package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
	"github.com/peterho/concertfinder/internal/jobs"
	"github.com/peterho/concertfinder/internal/picks"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/testdb"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// genreStub serves a genre listing of `pages` full pages, ten shows a day,
// all in lower Manhattan, and records every request.
type genreStub struct {
	mu    sync.Mutex
	reqs  []url.URL
	pages int
	start time.Time
	srv   *httptest.Server
}

func newGenreStub(t *testing.T, pages int) *genreStub {
	s := &genreStub{pages: pages, start: time.Now().Add(48 * time.Hour).UTC()}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, *r.URL)
		s.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/events.json") || r.URL.Query().Get("genreId") == "" {
			http.Error(w, "not stubbed", http.StatusBadRequest)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		events := make([]any, 0, 100)
		for i := 0; i < 100; i++ {
			n := page*100 + i
			events = append(events, map[string]any{
				"id":   fmt.Sprintf("jazz-%d", n),
				"name": fmt.Sprintf("Trio %d", n),
				"url":  fmt.Sprintf("https://www.ticketmaster.com/event/jazz-%d", n),
				"dates": map[string]any{
					"start":    map[string]any{"localDate": s.start.AddDate(0, 0, n/10).Format("2006-01-02"), "localTime": "20:00:00"},
					"timezone": "America/New_York",
				},
				"classifications": []any{map[string]any{
					"primary": true,
					"genre":   map[string]any{"id": "KnvZfZ7vAvE", "name": "Jazz"},
				}},
				"_embedded": map[string]any{
					"venues": []any{map[string]any{
						"name":     fmt.Sprintf("Club %d", n),
						"city":     map[string]any{"name": "New York"},
						"location": map[string]any{"latitude": "40.7306", "longitude": "-74.0004"},
					}},
					"attractions": []any{
						map[string]any{"id": fmt.Sprintf("Ktrio%d", n), "name": fmt.Sprintf("Trio %d", n)},
						map[string]any{"id": "Kseat", "name": "Club Level Seating: Blue Note"},
					},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"_embedded": map[string]any{"events": events},
			"page":      map[string]any{"size": 100, "totalElements": s.pages * 100, "totalPages": s.pages, "number": page},
		})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *genreStub) requests() []url.URL {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.URL(nil), s.reqs...)
}

func (s *genreStub) client() *ticketmaster.Client {
	base, _ := url.Parse(s.srv.URL)
	return ticketmaster.NewClient(&http.Client{Transport: hostRewrite{base: base}, Timeout: 10 * time.Second}, "test-key")
}

// CF-31 end to end: a user with nothing but genre picks is scanned, two users
// in one grid cell cost one fetch, the listing is capped with its horizon
// stated in the feed, seat packages are not acts, and a 600-show genre feed
// mails no more than the digest cap with a link to the rest.
func TestGenreOnlyUsersShareOneCappedListing(t *testing.T) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()
	// Six pages of New York jazz on offer: 600 shows.
	stub := newGenreStub(t, 6)
	outbound := guardOutbound(t)

	locA := concerts.Location{Latitude: 40.7128, Longitude: -74.0060, RadiusMiles: 50}
	locB := concerts.Location{Latitude: 40.7306, Longitude: -73.9866, RadiusMiles: 50}
	userA, tokenA := newPicksUser(t, pool, locA)
	userB, _ := newPicksUser(t, pool, locB)

	// With nothing picked the feed asks for picks; one genre is enough.
	if _, st := loadPickState(ctx, pool, auth.CurrentUser{ID: userA}); st != needsPicks {
		t.Fatalf("pick state before any pick = %v, want needsPicks", st)
	}
	for _, u := range []uuid.UUID{userA, userB} {
		if ok, err := db.AddGenrePick(ctx, pool, u, db.GenrePick{GenreID: "KnvZfZ7vAvE", Name: "Jazz"}); err != nil || !ok {
			t.Fatalf("add genre pick: ok=%v err=%v", ok, err)
		}
	}
	if _, st := loadPickState(ctx, pool, auth.CurrentUser{ID: userA}); st != hasSubject {
		t.Fatalf("pick state with a genre pick = %v, want hasSubject: a genre-only user must be scanned", st)
	}

	w := picksScanWorker(pool, stub.client(), nil)
	runScan(t, w, userA, locA)

	if len(outbound.seen) > 0 {
		t.Fatalf("genre scan made outbound requests: %v", outbound.seen)
	}
	reqs := stub.requests()
	if len(reqs) != ticketmaster.MaxGenrePages {
		t.Fatalf("ticketmaster requests = %d, want %d (stop at three pages)", len(reqs), ticketmaster.MaxGenrePages)
	}
	q := reqs[0].Query()
	if q.Get("genreId") != "KnvZfZ7vAvE" || q.Get("sort") != "date,asc" || q.Get("latlong") != "40.7500,-74.0000" {
		t.Errorf("listing query = %s, want genreId, date ascending, on the snapped cell", reqs[0].RawQuery)
	}
	var charged int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(count), 0) FROM rate_ledger WHERE user_id = $1 AND source = $2`,
		userA, string(rate.SourceTicketmaster)).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	if charged != ticketmaster.MaxGenrePages {
		t.Errorf("permits charged = %d, want one per page", charged)
	}

	snapA, hit, err := db.GetConcertSnapshot(ctx, pool, userA, jobs.LocationKey(locA))
	if err != nil || !hit {
		t.Fatalf("snapshot: hit=%v err=%v", hit, err)
	}
	if !snapA.Complete {
		t.Error("a listing cut at its page cap is complete up to its horizon, not an incomplete scan")
	}
	if snapA.GenreHorizon == "" {
		t.Fatal("snapshot carries no genre horizon for a capped listing")
	}
	if n := len(snapA.DedupKeys); n == 0 || n > concerts.MaxGenreFeedConcerts {
		t.Fatalf("snapshot holds %d shows, want 1..%d", n, concerts.MaxGenreFeedConcerts)
	}
	refs, err := concerts.DecodeArtistRefs(snapA.ArtistRefs)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if strings.Contains(ref.Name, "Seating") {
			t.Fatalf("a seat package became an act: %+v", ref)
		}
		if !strings.HasPrefix(ref.ID, "tm:Ktrio") || len(ref.Genres) != 1 || ref.Genres[0] != "Jazz" {
			t.Fatalf("ref = %+v, want a tm: attraction ID and Ticketmaster's genre", ref)
		}
	}

	// The second user in the same cell is served from the first one's fetch.
	runScan(t, w, userB, locB)
	if n := len(stub.requests()); n != ticketmaster.MaxGenrePages {
		t.Errorf("second user in the cell made %d more requests, want 0", n-ticketmaster.MaxGenrePages)
	}
	snapB, hit, err := db.GetConcertSnapshot(ctx, pool, userB, jobs.LocationKey(locB))
	if err != nil || !hit || len(snapB.DedupKeys) == 0 || snapB.GenreHorizon != snapA.GenreHorizon {
		t.Fatalf("second user's snapshot: hit=%v keys=%d horizon=%q err=%v", hit, len(snapB.DedupKeys), snapB.GenreHorizon, err)
	}

	// The feed says it is a capped listing, and nothing on or after the
	// horizon day is in it.
	rec := request(t, feedRouter(pool, locA), "GET", "/me/concerts", tokenA)
	if rec.Code != 200 {
		t.Fatalf("GET /me/concerts: %d %s", rec.Code, rec.Body.String())
	}
	var feed concertsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.NeedsPicks || feed.GenreHorizon != snapA.GenreHorizon {
		t.Fatalf("feed needs_picks=%v genre_horizon=%q, want false and %q", feed.NeedsPicks, feed.GenreHorizon, snapA.GenreHorizon)
	}
	for _, e := range feed.Events {
		if e.LocalDay() >= feed.GenreHorizon {
			t.Fatalf("event on %s is past the horizon %s", e.LocalDay(), feed.GenreHorizon)
		}
	}
	if len(feed.Facets.Genres) != 1 || feed.Facets.Genres[0].Value != "Jazz" {
		t.Errorf("genre facets = %+v, want Jazz", feed.Facets.Genres)
	}

	// The digest lists at most the cap and links to the feed for the rest,
	// and only what it listed is recorded as sent.
	digest := &capturingSender{}
	dw := &jobs.SendDigestWorker{
		Pool: pool, Sender: digest, UnsubscribeBase: "https://example.test",
		UnsubscribeToken: func(uuid.UUID) string { return "tok" },
		Fallback:         jobs.FallbackLocation{Latitude: locA.Latitude, Longitude: locA.Longitude, RadiusMiles: locA.RadiusMiles},
	}
	if err := dw.Work(ctx, &river.Job[jobs.SendDigestArgs]{JobRow: &rivertype.JobRow{}, Args: jobs.SendDigestArgs{UserID: userA}}); err != nil {
		t.Fatalf("digest: %v", err)
	}
	msgs := digest.all()
	if len(msgs) != 1 {
		t.Fatalf("%d digests, want 1", len(msgs))
	}
	listed := strings.Count(msgs[0].Text, "    ticketmaster: ")
	if listed == 0 || listed > email.DigestMaxEvents {
		t.Errorf("digest lists %d shows, want 1..%d", listed, email.DigestMaxEvents)
	}
	if !strings.Contains(msgs[0].Text, "more. See them all: https://example.test/") {
		t.Errorf("digest does not link to the rest:\n%s", msgs[0].Text)
	}
	sent, err := db.CountDigestSent(ctx, pool, userA, db.ChannelEmail)
	if err != nil {
		t.Fatal(err)
	}
	if sent != listed {
		t.Errorf("recorded %d as sent, but the digest listed %d: the rest must stay unsent for the next one", sent, listed)
	}
}

// The genre cap is enforced in the insert, as the artist cap is.
func TestGenrePickCap(t *testing.T) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()
	userID, _ := newPicksUser(t, pool, concerts.Location{Latitude: 40.7, Longitude: -74, RadiusMiles: 50})
	for i := 0; i < db.MaxGenrePicks; i++ {
		if ok, err := db.AddGenrePick(ctx, pool, userID, db.GenrePick{GenreID: fmt.Sprintf("g%d", i), Name: "G"}); err != nil || !ok {
			t.Fatalf("pick %d: ok=%v err=%v", i, ok, err)
		}
	}
	if ok, err := db.AddGenrePick(ctx, pool, userID, db.GenrePick{GenreID: "one-too-many", Name: "G"}); err != nil || ok {
		t.Errorf("pick past the cap: ok=%v err=%v, want refused", ok, err)
	}
	if ok, err := db.AddGenrePick(ctx, pool, userID, db.GenrePick{GenreID: "g0", Name: "Renamed"}); err != nil || !ok {
		t.Errorf("refreshing a held pick on a full list: ok=%v err=%v, want allowed", ok, err)
	}
}

// POST /me/genres takes only an ID, accepts only a catalog genre, and stores
// Ticketmaster's name for it.
func TestAddGenrePickValidatesAgainstTheCatalog(t *testing.T) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()
	userID, token := newPicksUser(t, pool, concerts.Location{Latitude: 40.7, Longitude: -74, RadiusMiles: 50})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": ticketmaster.MusicSegmentID, "name": "Music",
			"_embedded": map[string]any{"genres": []any{map[string]any{"id": "KnvZfZ7vAvE", "name": "Jazz"}}},
		})
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	tm := ticketmaster.NewClient(&http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}, "test-key")
	h := &GenrePicksHandler{Pool: pool, Catalog: &picks.GenreCatalog{TM: tm}}
	r := chi.NewRouter()
	r.Route("/me", func(r chi.Router) {
		r.Use(auth.RequireUser(pool))
		r.Post("/genres", h.Add)
	})
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/me/genres", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"genre_id":"KnvZfZ7vAvE","name":"Anything"}`); rec.Code != http.StatusCreated {
		t.Fatalf("add Jazz: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"genre_id":"KZazBEonSMnZfZ7vkE1"}`); rec.Code != http.StatusNotFound {
		t.Errorf("add a sub-genre: %d, want 404", rec.Code)
	}
	ps, err := db.ListGenrePicks(ctx, pool, userID)
	if err != nil || len(ps) != 1 || ps[0].Name != "Jazz" {
		t.Errorf("picks = %+v err=%v, want only Jazz under Ticketmaster's name", ps, err)
	}
}
