package spotify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeAPI answers Spotify requests without a network. Each endpoint returns an
// endless `next` chain, so a client with no page cap would paginate forever —
// which is exactly the behavior under test.
type fakeAPI struct {
	calls atomic.Int64
	// failPath, when non-empty, makes any request whose URL contains it
	// return 500 on every attempt.
	failPath string
}

func (f *fakeAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	u := r.URL.String()
	reply := func(code int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: code,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    r,
		}, nil
	}
	if f.failPath != "" && strings.Contains(u, f.failPath) {
		return reply(500, `{"error":"boom"}`)
	}
	// A `next` that always points somewhere new.
	next := fmt.Sprintf("%s%coffset=%d", u, sepFor(u), f.calls.Load())
	switch {
	case strings.Contains(u, "/me/tracks"):
		return reply(200, fmt.Sprintf(`{"items":[{"track":{"artists":[{"id":"t1","name":"T"}]}}],"next":%q}`, next))
	case strings.Contains(u, "/me/albums"):
		return reply(200, fmt.Sprintf(`{"items":[{"album":{"artists":[{"id":"a1","name":"A"}]}}],"next":%q}`, next))
	case strings.Contains(u, "/me/following"):
		return reply(200, fmt.Sprintf(`{"artists":{"items":[{"id":"f1","name":"F"}],"next":%q}}`, next))
	case strings.Contains(u, "/me/playlists"):
		return reply(200, fmt.Sprintf(`{"items":[{"id":"p1","owner":{"id":"me"}}],"next":%q}`, next))
	case strings.Contains(u, "/items"):
		return reply(200, fmt.Sprintf(`{"items":[{"track":{"artists":[{"id":"pi","name":"P"}]}}],"next":%q}`, next))
	case strings.Contains(u, "recently-played"):
		return reply(200, `{"items":[{"track":{"artists":[{"id":"r1","name":"R"}]}}]}`)
	case strings.Contains(u, "/me/top/artists"):
		return reply(200, `{"items":[{"id":"top1","name":"Top","genres":["rock"]}]}`)
	}
	return reply(404, `{}`)
}

func sepFor(u string) rune {
	if strings.Contains(u, "?") {
		return '&'
	}
	return '?'
}

func newFakeClient(f *fakeAPI) *Client {
	return NewClient(&http.Client{Transport: f})
}

// Every paginated source used to walk a library of any size, sequentially,
// inside a 60s compute budget. A user with a big enough library therefore
// timed out every time — and because the scan worker computes affinity before
// it searches, that user's feed stayed empty permanently. The size of
// someone's library silently decided whether the product worked for them.
func TestPaginationIsCapped(t *testing.T) {
	cases := []struct {
		name     string
		maxPages int
		call     func(*Client) error
	}{
		{"saved_tracks", maxSavedTrackPages, func(c *Client) error {
			_, err := c.SavedTracks(context.Background(), "tok")
			return err
		}},
		{"saved_albums", maxSavedAlbumPages, func(c *Client) error {
			_, err := c.SavedAlbums(context.Background(), "tok")
			return err
		}},
		{"followed", maxFollowedPages, func(c *Client) error {
			_, err := c.FollowedArtists(context.Background(), "tok")
			return err
		}},
		{"playlists", maxPlaylistPages, func(c *Client) error {
			_, err := c.UserPlaylists(context.Background(), "tok")
			return err
		}},
		{"playlist_items", maxPlaylistItemPages, func(c *Client) error {
			_, err := c.PlaylistItems(context.Background(), "tok", "p1")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{}
			if err := tc.call(newFakeClient(f)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := int(f.calls.Load()); got != tc.maxPages {
				t.Errorf("made %d requests against an endless feed, want the cap of %d",
					got, tc.maxPages)
			}
		})
	}
}

// One failing source degrades the profile; it must not destroy it. The old
// errgroup aborted the other five on the first error and returned nothing, so
// a single Spotify 500 meant no profile at all — and no feed.
func TestHydrateSurvivesOneFailingSource(t *testing.T) {
	f := &fakeAPI{failPath: "/me/albums"}
	s, errs := newFakeClient(f).HydrateSources(context.Background(), "tok", "me")
	if len(errs) == 0 {
		t.Error("the failing source should be reported to the caller for logging")
	}
	if len(s.Followed) == 0 {
		t.Error("followed artists were lost to an unrelated source's failure")
	}
	if len(s.SavedTracks) == 0 {
		t.Error("saved tracks were lost to an unrelated source's failure")
	}
	if len(s.Top.Short) == 0 {
		t.Error("top artists were lost to an unrelated source's failure")
	}
	// The surviving signal has to actually produce a profile.
	if len(ScoreArtists(s)) == 0 {
		t.Error("partial hydration produced no scored artists")
	}
}

// Total failure is still an error: scoring nothing would persist an empty
// profile for the full 24h TTL and leave the scan worker nothing to search.
func TestHydrateReportsTotalFailure(t *testing.T) {
	f := &fakeAPI{failPath: "/"} // every path
	s, errs := newFakeClient(f).HydrateSources(context.Background(), "tok", "me")
	if len(errs) == 0 {
		t.Fatal("expected errors when every source fails")
	}
	if len(ScoreArtists(s)) != 0 {
		t.Error("expected no signal at all")
	}
}
