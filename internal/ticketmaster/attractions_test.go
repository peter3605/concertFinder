package ticketmaster

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Ticketmaster ranks /attractions.json by its own relevance, and for a
// working band the top hit is routinely a tribute act. ResolveAttraction
// therefore ignores the ranking entirely and takes the exact
// case-insensitive name match wherever it appears -- the false positives
// this avoids are the reason resolution is two-stage at all.
func TestResolveAttractionTakesTheExactMatchNotTheTopHit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		query   string
		fixture string
		want    string
		why     string
	}{
		{
			name:    "an exact match ranked third beats the tribute act ranked first",
			query:   "Test Artist",
			fixture: "attractions_match.json",
			want:    "K8vZ917realone",
			why:     "taking _embedded.attractions[0] would book the user tickets to a cover band",
		},
		{
			name:    "the comparison folds case on both sides",
			query:   "test artist",
			fixture: "attractions_match.json",
			want:    "K8vZ917realone",
			why:     "TM stores the attraction as TEST ARTIST; Spotify's display name is not authoritative about capitalisation",
		},
		{
			name:    "surrounding whitespace in the artist name is trimmed before comparing",
			query:   "  Test Artist  ",
			fixture: "attractions_match.json",
			want:    "K8vZ917realone",
			why:     "the name arrives from a Spotify profile, not from a form we control",
		},
		{
			name:    "substring hits are not a match",
			query:   "Test Artist",
			fixture: "attractions_nomatch.json",
			want:    "",
			why:     "'Test Artist Tribute' and 'Testing Artist' both contain the query; a substring rule is exactly the false-positive path",
		},
		{
			name:    "a response with no attractions block resolves to nothing",
			query:   "Test Artist",
			fixture: "attractions_empty.json",
			want:    "",
			why:     "an artist TM has never heard of; the caller caches this as a negative resolution",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestAPI(t, serveFixture(t, tc.fixture))
			got, err := c.ResolveAttraction(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("ResolveAttraction: %v (%s)", err, tc.why)
			}
			if got != tc.want {
				t.Errorf("attractionId = %q, want %q (%s)", got, tc.want, tc.why)
			}
		})
	}
}

// A no-match must be ("", nil), never an error. concerts/search.go only
// persists an artist_resolutions row -- the negative cache entry that
// NegativeResolutionTTL later expires -- when the call returned no error, so
// erroring here would mean re-asking Ticketmaster about the same unknown
// artist on every scan, forever, at one rate permit each.
func TestResolveAttractionReportsANoMatchAsAnEmptyIDAndNoError(t *testing.T) {
	c, _ := newTestAPI(t, serveFixture(t, "attractions_nomatch.json"))
	id, err := c.ResolveAttraction(context.Background(), "Test Artist")
	if err != nil {
		t.Fatalf("a negative resolution is not an error: %v", err)
	}
	if id != "" {
		t.Errorf("attractionId = %q, want empty", id)
	}
}

func TestResolveAttractionQueriesTheAttractionsEndpoint(t *testing.T) {
	c, rec := newTestAPI(t, serveFixture(t, "attractions_match.json"))
	if _, err := c.ResolveAttraction(context.Background(), "Test Artist"); err != nil {
		t.Fatalf("ResolveAttraction: %v", err)
	}
	reqs := rec.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(reqs))
	}
	if reqs[0].Path != "/discovery/v2/attractions.json" {
		t.Errorf("path = %q, want /discovery/v2/attractions.json", reqs[0].Path)
	}
	q := reqs[0].Query()
	assertQuery(t, q, "keyword", "Test Artist")
	// classificationName=Music is not decoration: without it the keyword
	// matches sports teams and comedians sharing a band's name.
	assertQuery(t, q, "classificationName", "Music")
	assertQuery(t, q, "size", "10")
	assertQuery(t, q, "apikey", secret)
}

// An empty artist name never reaches the wire. Same reasoning as the empty
// attractionId in SearchEvents: the call would spend a rate permit to ask
// Ticketmaster about nobody.
func TestResolveAttractionSkipsTheCallForAnEmptyName(t *testing.T) {
	c, rec := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("ResolveAttraction contacted the API with an empty name")
		w.WriteHeader(http.StatusInternalServerError)
	})
	id, err := c.ResolveAttraction(context.Background(), "")
	if err != nil || id != "" {
		t.Fatalf("ResolveAttraction(\"\") = %q, %v; want \"\", nil", id, err)
	}
	if rec.count() != 0 {
		t.Errorf("expected 0 requests, got %d", rec.count())
	}
}

// The two stages end to end: resolve the name to an attractionId, then query
// events filtered by that ID. The assertion that matters is the handoff --
// stage two must carry the ID stage one returned and must not fall back to
// the artist name.
func TestArtistResolutionIsTwoStageAndNeverKeywordSearchesEvents(t *testing.T) {
	attractions := fixture(t, "attractions_match.json")
	events := fixture(t, "events_lineup.json")

	c, rec := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/discovery/v2/attractions.json":
			_, _ = w.Write(attractions)
		case "/discovery/v2/events.json":
			_, _ = w.Write(events)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	})

	ctx := context.Background()
	id, err := c.ResolveAttraction(ctx, "Test Artist")
	if err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	if id == "" {
		t.Fatal("stage 1 resolved nothing; the rest of the flow cannot run")
	}
	evs, err := c.SearchEvents(ctx, id, testLat, testLng, testRadius)
	if err != nil {
		t.Fatalf("stage 2: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("stage 2 returned no events")
	}

	reqs := rec.requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests (resolve, then search), got %d", len(reqs))
	}
	if reqs[0].Path != "/discovery/v2/attractions.json" {
		t.Errorf("first request = %q, want the attractions endpoint", reqs[0].Path)
	}
	if reqs[1].Path != "/discovery/v2/events.json" {
		t.Errorf("second request = %q, want the events endpoint", reqs[1].Path)
	}
	if got := reqs[1].Query().Get("attractionId"); got != id {
		t.Errorf("events query attractionId = %q, want the resolved %q", got, id)
	}
	if reqs[1].Query().Has("keyword") {
		t.Error("the events query fell back to a keyword, which reintroduces the tribute-act false positives resolution exists to remove")
	}
}

// ResolveAttraction wraps doGETRetry's error with the artist name, and that
// wrapped form is what concerts/search.go logs. client_test.go synthesizes
// the wrapping; this exercises the real one.
func TestResolveAttractionErrorDoesNotLeakTheAPIKey(t *testing.T) {
	c, _ := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})
	_, err := c.ResolveAttraction(context.Background(), "Test Artist")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("api key leaked into error: %q", err)
	}
	if !strings.Contains(err.Error(), "Test Artist") {
		t.Errorf("the artist name is what makes the log line actionable: %q", err)
	}
}

func TestResolveAttractionReportsAMalformedBody(t *testing.T) {
	c, _ := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("<html>gateway error</html>"))
	})
	id, err := c.ResolveAttraction(context.Background(), "Test Artist")
	if err == nil {
		t.Fatal("a body we cannot decode is an error, not a negative resolution -- caching it would hide the artist until NegativeResolutionTTL expires")
	}
	if id != "" {
		t.Errorf("attractionId = %q, want empty on a decode failure", id)
	}
	if !strings.Contains(err.Error(), "decode attractions") {
		t.Errorf("error should name the decode step: %q", err)
	}
}
