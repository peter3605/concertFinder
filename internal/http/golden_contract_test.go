package http

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/picks"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// The contract check between the Go handlers and the Swift client
// (docs/ios-app-plan.md §8).
//
// Two clients and no contract test is how a field rename reaches the App
// Store: renaming a JSON tag here is a one-commit operation on the server and
// a silent decode failure on a build already in someone's pocket, months
// later, with nothing in between to catch it.
//
// The mechanism is deliberately the cheap one the plan suggests rather than a
// cross-language type checker. These tests marshal the **real** response
// structs — not hand-written literals — into golden files under
// ios/ConcertFinderTests/Fixtures, which the Swift tests then decode. So:
//
//   - Change a json tag in Go and this test fails, because the golden file no
//     longer matches. Regenerating is one command.
//   - Regenerate without updating the Swift models and ModelDecodingTests
//     fails, because the fixture no longer decodes.
//
// Neither half is skippable, and the failure lands in CI rather than at
// review. Regenerate with:
//
//	go test ./internal/http -run TestGoldenFixtures -update

var updateGolden = flag.Bool("update", false, "rewrite the golden fixtures the Swift tests decode")

// fixtureDir is where the iOS test bundle reads its fixtures from. A relative
// path rather than a build setting because the two trees live in one repo,
// which is the whole reason this check is cheap (plan §5.2).
const fixtureDir = "../../ios/ConcertFinderTests/Fixtures"

func TestGoldenFixtures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload any
	}{
		{"festival", goldenFestival()},
		{"empty-feed", goldenEmptyFeed()},
		{"incomplete-scan", goldenIncompleteScan()},
		{"refresh-throttled", goldenRefreshThrottled()},
		{"discover", goldenDiscover()},
		// CF-37: the shapes an account without Spotify meets.
		{"picks-feed", goldenPicksFeed()},
		{"needs-picks", goldenNeedsPicks()},
		{"artist-picks", goldenArtistPicks()},
		{"pick-search", goldenPickSearch()},
		{"genre-picks", goldenGenrePicks()},
		{"genre-catalog", goldenGenreCatalog()},
		{"site-info", goldenSiteInfo(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Indented and newline-terminated so a diff on failure is
			// readable and the files are reviewable in a PR.
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetIndent("", "  ")
			if err := enc.Encode(tc.payload); err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			got := buf.Bytes()
			path := filepath.Join(fixtureDir, tc.name+".json")

			if *updateGolden {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				t.Logf("updated %s", path)
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v (run: go test ./internal/http -run TestGoldenFixtures -update)", path, err)
			}
			if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
				t.Errorf(
					"golden fixture %s is stale — the Go response shape changed.\n\n"+
						"Regenerate:  go test ./internal/http -run TestGoldenFixtures -update\n"+
						"Then run the Swift tests: a fixture that no longer decodes means the\n"+
						"iOS models in ios/ConcertFinder/Core/Models need the same change.\n\n"+
						"--- got ---\n%s\n--- want ---\n%s",
					tc.name, got, want,
				)
			}
		})
	}
}

// TestFeedLocationHasNoDisplayFields pins a distinction that is invisible from
// the client and easy to get wrong in either direction.
//
// GET /me/concerts serializes concerts.Location — latitude, longitude, radius
// and nothing else. The display_name and is_default fields live only on
// GET /me/location's own DTO. A client that reads is_default off the feed
// response gets false every time, so a "set your location" prompt driven from
// there silently never fires and the user is shown the deployment's fallback
// city as if they had chosen it.
func TestFeedLocationHasNoDisplayFields(t *testing.T) {
	body, err := json.Marshal(concerts.Location{Latitude: 38.8951, Longitude: -77.0364, RadiusMiles: 50})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"display_name", "is_default"} {
		if _, present := fields[absent]; present {
			t.Errorf("concerts.Location now serializes %q — the feed and /me/location "+
				"payloads have converged, so the iOS UserLocation model and the "+
				"comment in FeedView should be revisited", absent)
		}
	}
	for _, required := range []string{"latitude", "longitude", "radius_miles"} {
		if _, present := fields[required]; !present {
			t.Errorf("concerts.Location no longer serializes %q", required)
		}
	}
}

// TestEventCarriesCoordinates pins the opposite case: Event *does* carry
// latitude and longitude, so a client has no reason to make Maps geocode a
// venue string. They are omitempty, so a fixture without them is still valid
// and the client must treat them as optional.
func TestEventCarriesCoordinates(t *testing.T) {
	body, err := json.Marshal(concerts.Event{
		EventKey:  "k",
		Venue:     "9:30 Club",
		Latitude:  38.9180,
		Longitude: -77.0243,
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"latitude", "longitude"} {
		if _, present := fields[required]; !present {
			t.Errorf("concerts.Event no longer serializes %q — the iOS event detail "+
				"screen uses it to drop an exact Maps pin instead of searching "+
				"for a venue name", required)
		}
	}
}

// --- fixture payloads -------------------------------------------------------
//
// Built from the real structs. Values are representative rather than captured
// from production, because a captured response would carry a real user's
// listening profile — but the *shapes* are the handlers' own.

func goldenAct(id, name, dedup string, genres []string, saved, subscribed bool, reason string) concerts.Act {
	return concerts.Act{
		Artist:     concerts.ArtistRef{ID: id, Name: name, Genres: genres},
		DedupKey:   dedup,
		Saved:      saved,
		Subscribed: subscribed,
		Reason:     reason,
	}
}

// goldenFestival is the shape most likely to be got wrong: six of the user's
// artists on one bill is ONE card with six acts, each keeping its own
// dedup_key so save and subscribe stay per artist.
func goldenFestival() concertsResponse {
	computed := time.Date(2026, 8, 23, 9, 14, 22, 418_000_000, time.UTC)
	return concertsResponse{
		Location: concerts.Location{Latitude: 38.8951, Longitude: -77.0364, RadiusMiles: 50},
		Count:    1,
		Events: []concerts.Event{{
			EventKey:  "9f2c1a4b8e7d6c5a4b3f2e1d0c9b8a77",
			Date:      time.Date(2026, 7, 4, 18, 0, 0, 0, time.UTC),
			Venue:     "Merriweather Post Pavilion",
			City:      "Columbia",
			State:     "MD",
			Country:   "US",
			Latitude:  39.2037,
			Longitude: -76.8610,
			Acts: []concerts.Act{
				goldenAct("3fMbdgg4jU18AjLCKBhRSm", "Turnstile", "a1b2c3d4e5f60718293a4b5c6d7e8f90", []string{"hardcore punk", "alternative rock"}, true, false, "You follow them"),
				goldenAct("4LG4Bs1Gadht7TCrMytQUO", "Snail Mail", "b2c3d4e5f60718293a4b5c6d7e8f9001", []string{"indie rock"}, false, true, "#7 in your top artists"),
				goldenAct("56ZTgzPBDge0OvCGgMO3OY", "Beach House", "c3d4e5f60718293a4b5c6d7e8f900112", []string{"dream pop"}, false, false, "You saved 3 of their albums"),
				goldenAct("1Bl6wpkWCQ4KVgnASpvzzA", "Wednesday", "d4e5f60718293a4b5c6d7e8f90011223", []string{"indie rock"}, false, false, "Recently played"),
				goldenAct("6l3HvQ5sa6mXTsMTB19rO5", "MJ Lenderman", "e5f60718293a4b5c6d7e8f9001122334", []string{"indie rock"}, false, false, "In 2 of your playlists"),
				goldenAct("7jy3rLJdDQY21OgRLCZ9sD", "Foo Fighters", "f60718293a4b5c6d7e8f900112233445", []string{"alternative rock"}, false, false, ""),
			},
			Links: []concerts.TicketLink{
				{Source: "ticketmaster", URL: "https://www.ticketmaster.com/event/example"},
				{Source: "official", URL: "https://example.com/tour"},
			},
		}},
		Facets: facetSet{
			Genres: []facet{{Value: "indie rock", Count: 1}, {Value: "alternative rock", Count: 1}},
			Venues: []facet{{Value: "Merriweather Post Pavilion", Count: 1}},
		},
		ComputedAt: &computed,
		Refreshing: false,
		Complete:   true,
	}
}

func goldenEmptyFeed() concertsResponse {
	computed := time.Date(2026, 8, 23, 9, 14, 22, 0, time.UTC)
	return concertsResponse{
		Location:   concerts.Location{Latitude: 38.8951, Longitude: -77.0364, RadiusMiles: 50},
		Count:      0,
		Events:     []concerts.Event{},
		Facets:     facetSet{Genres: []facet{}, Venues: []facet{}},
		ComputedAt: &computed,
		Refreshing: false,
		Complete:   true,
	}
}

// goldenIncompleteScan is the state the UI must tell apart from a quiet week:
// the scan did not cover every artist, and the shortfall was the daily
// upstream quota, so retry_after says when it is worth trying again.
func goldenIncompleteScan() concertsResponse {
	computed := time.Date(2026, 8, 23, 6, 2, 11, 0, time.UTC)
	retry := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	return concertsResponse{
		Location: concerts.Location{Latitude: 38.8951, Longitude: -77.0364, RadiusMiles: 50},
		Count:    1,
		Events: []concerts.Event{{
			EventKey:  "11223344556677889900112233445566",
			Date:      time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
			Venue:     "9:30 Club",
			City:      "Washington",
			State:     "DC",
			Country:   "US",
			Latitude:  38.9180,
			Longitude: -77.0243,
			Acts: []concerts.Act{
				goldenAct("3fMbdgg4jU18AjLCKBhRSm", "Turnstile", "aabbccddeeff00112233445566778899", []string{"hardcore punk"}, false, false, "You follow them"),
			},
			Links: []concerts.TicketLink{
				{Source: "ticketmaster", URL: "https://www.ticketmaster.com/event/other"},
			},
		}},
		Facets: facetSet{
			Genres: []facet{{Value: "hardcore punk", Count: 1}},
			Venues: []facet{{Value: "9:30 Club", Count: 1}},
		},
		ComputedAt: &computed,
		Refreshing: true,
		Complete:   false,
		RetryAfter: &retry,
	}
}

// goldenRefreshThrottled is the 429 body. The client shows both fields:
// retry_after is when it lifts, and reason is what distinguishes "you just
// refreshed" from "today's upstream allowance is gone".
func goldenRefreshThrottled() refreshResponse {
	retry := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	return refreshResponse{
		Refreshing: false,
		RetryAfter: &retry,
		Reason:     "daily upstream quota exhausted",
	}
}

// goldenDiscover is GET /api/discover: the signed-out view. It reuses the
// Event model, which is the point — but it is a *different* response type, so
// a client cannot assume the feed's fields are there. There is no facets
// block, no computed_at, no refreshing and no complete flag, and its acts
// carry no artist id, no reason and no saved/subscribed state, because
// nothing about this response knows who is asking.
func goldenDiscover() discoverResponse {
	return discoverResponse{
		Location: concerts.Location{Latitude: 38.8951, Longitude: -77.0364, RadiusMiles: 50},
		Count:    1,
		Events: []concerts.Event{{
			EventKey:  "22334455667788990011223344556677",
			Date:      time.Date(2026, 10, 3, 19, 30, 0, 0, time.UTC),
			Venue:     "The Anthem",
			City:      "Washington",
			State:     "DC",
			Country:   "US",
			Latitude:  38.8790,
			Longitude: -77.0217,
			Acts: []concerts.Act{
				{Artist: concerts.ArtistRef{Name: "Fontaines D.C."}, DedupKey: "0011223344556677889900aabbccddee", Billing: "headliner"},
				{Artist: concerts.ArtistRef{Name: "Been Stellar"}, DedupKey: "11223344556677889900aabbccddeeff", Billing: "support"},
			},
			Links: []concerts.TicketLink{
				{Source: "ticketmaster", URL: "https://www.ticketmaster.com/event/anthem"},
			},
		}},
	}
}

// goldenPicksFeed is a feed built from picks (CF-30, CF-31): acts carry the
// "tm:<attraction>" ID a pick's subscription uses rather than a Spotify ID,
// carry no reason line (there is no affinity profile to explain them), and
// genre_horizon says where the capped genre listing stops.
func goldenPicksFeed() concertsResponse {
	computed := time.Date(2026, 11, 20, 4, 30, 0, 0, time.UTC)
	return concertsResponse{
		Location: concerts.Location{Latitude: 40.7128, Longitude: -74.006, RadiusMiles: 25},
		Count:    1,
		Events: []concerts.Event{{
			EventKey:  "33445566778899001122334455667788",
			Date:      time.Date(2026, 12, 4, 0, 30, 0, 0, time.UTC),
			LocalDate: "2026-12-03",
			Venue:     "Village Vanguard",
			City:      "New York",
			State:     "NY",
			Country:   "US",
			Latitude:  40.7359,
			Longitude: -74.0016,
			Acts: []concerts.Act{
				goldenAct("tm:K8vZ917Gku7", "Brad Mehldau", "8899aabbccddeeff0011223344556677", []string{"Jazz"}, false, true, ""),
			},
			Links: []concerts.TicketLink{
				{Source: "ticketmaster", URL: "https://www.ticketmaster.com/event/vanguard"},
			},
		}},
		Facets: facetSet{
			Genres: []facet{{Value: "Jazz", Count: 1}},
			Venues: []facet{{Value: "Village Vanguard", Count: 1}},
		},
		ComputedAt:   &computed,
		Refreshing:   false,
		Complete:     true,
		GenreHorizon: "2027-01-15",
	}
}

// goldenNeedsPicks is an account with no Spotify identity and nothing
// picked: nothing to scan, nothing queued. The app must route to the picker
// rather than poll, because refreshing is false and stays false.
func goldenNeedsPicks() concertsResponse {
	return concertsResponse{
		Location:   concerts.Location{Latitude: 40.7128, Longitude: -74.006, RadiusMiles: 25},
		Count:      0,
		Events:     []concerts.Event{},
		Facets:     facetSet{Genres: []facet{}, Venues: []facet{}},
		Refreshing: false,
		Complete:   true,
		NeedsPicks: true,
	}
}

func goldenArtistPicks() picksListResponse {
	return picksListResponse{
		Picks: []db.ArtistPick{
			{AttractionID: "K8vZ917Gku7", Name: "Brad Mehldau", Genre: "Jazz"},
			{AttractionID: "K8vZ9175Tr0", Name: "Ghost"},
		},
		Max: db.MaxArtistPicks,
	}
}

func goldenPickSearch() pickSearchResponse {
	return pickSearchResponse{
		Query: "ghost",
		Results: []picks.Result{
			{AttractionID: "K8vZ9175Tr0", Name: "Ghost", Genre: "Rock", ImageURL: "https://s1.ticketm.net/dam/a/ghost.jpg", Upcoming: 14, Exact: true},
			{AttractionID: "K8vZ917oWOV", Name: "Ghostface Killah", Genre: "Hip-Hop/Rap", Upcoming: 3},
		},
	}
}

func goldenGenrePicks() genrePicksResponse {
	return genrePicksResponse{
		Picks: []db.GenrePick{{GenreID: "KnvZfZ7vAvE", Name: "Jazz"}},
		Max:   db.MaxGenrePicks,
	}
}

func goldenGenreCatalog() genreCatalogResponse {
	return genreCatalogResponse{Genres: []ticketmaster.Genre{
		{ID: "KnvZfZ7vAvE", Name: "Jazz"},
		{ID: "KnvZfZ7vAeA", Name: "Rock"},
	}}
}

// goldenSiteInfo runs the real handler rather than restating its map, since
// the payload is a literal inside Get. With no pool, spotify_signups_open is
// decided by the cap alone, so the fixture needs no database.
func goldenSiteInfo(t *testing.T) json.RawMessage {
	t.Helper()
	h := &SiteInfoHandler{
		ContactEmail:    "contact@example.com",
		EffectiveDate:   "2026-11-21",
		MinIOSBuild:     3,
		InviteRequired:  true,
		MaxSpotifyUsers: 5,
		AppleSignIn:     true,
	}
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/api/site-info", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("site-info: status %d", rec.Code)
	}
	return json.RawMessage(bytes.TrimSpace(rec.Body.Bytes()))
}
