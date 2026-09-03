package spotify

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// MaxScoredArtists is the top-N cap submitted to concert search (design §4.3).
const MaxScoredArtists = 200

// Weights are starting values per design §4.3 — tune during Phase 1 dogfooding.
const (
	weightFollowed   = 1.0
	weightTop        = 0.9
	weightSavedAlbum = 0.7
	weightSavedTrack = 0.5
	weightRecent     = 0.4
	weightPlaylist   = 0.2
)

var timeRangeWeights = map[TimeRange]float64{
	ShortTerm:  1.0,
	MediumTerm: 0.8,
	LongTerm:   0.6,
}

// Sources bundles the raw per-source data fed to ScoreArtists. Held in memory
// only; never persisted (design §4.4).
type Sources struct {
	Followed      []ArtistRef
	Top           TopArtistsByRange
	SavedAlbums   []SavedAlbum
	SavedTracks   []SavedTrack
	Recent        []RecentPlay
	PlaylistItems [][]PlaylistItem // one slice per owned/collaborated playlist
}

type ScoredArtist struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Score  float64  `json:"score"`
	Genres []string `json:"genres,omitempty"`
	// Signals records which inputs produced Score. It is what lets the feed
	// say *why* an artist is in it; see affinity.Reason.
	Signals ArtistSignals `json:"signals"`
}

// ArtistSignals is the per-signal breakdown behind a ScoredArtist's score:
// which of the six §4.3 inputs contributed, and how much of each.
//
// This is derived affinity data — counts over signals that were already
// scored — not raw Spotify Content, so it belongs in the same profile blob
// as the scores and adds no new storage of listening data (design §4.4).
// Nothing here names a track, an album, or a playlist.
//
// Zero value means "computed before this existed": profiles persisted by an
// older build decode with every field empty, which reads as "no reason to
// show" rather than a wrong one.
type ArtistSignals struct {
	Followed bool `json:"followed,omitempty"`
	// TopRank is the best (lowest) 1-based position the artist held across
	// the three time ranges, or 0 if they were in none of them.
	TopRank     int `json:"top_rank,omitempty"`
	SavedAlbums int `json:"saved_albums,omitempty"`
	SavedTracks int `json:"saved_tracks,omitempty"`
	RecentPlays int `json:"recent_plays,omitempty"`
	// Playlists counts the user's own playlists the artist appears in, not
	// the number of their tracks in them.
	Playlists int `json:"playlists,omitempty"`
}

// ScoreArtists applies the §4.3 formula and returns the top MaxScoredArtists
// sorted by descending score. Ties broken by artist name for determinism.
// Genres are captured from top-artist entries (currently the only genre
// source) and unioned per artist.
func ScoreArtists(s Sources) []ScoredArtist {
	type accum struct {
		name   string
		score  float64
		genres map[string]struct{}
		sig    ArtistSignals
	}
	scores := map[string]*accum{}

	bump := func(a ArtistRef, delta float64) *accum {
		if a.ID == "" {
			return nil
		}
		if cur, ok := scores[a.ID]; ok {
			cur.score += delta
			if cur.name == "" && a.Name != "" {
				cur.name = a.Name
			}
			return cur
		}
		fresh := &accum{name: a.Name, score: delta, genres: map[string]struct{}{}}
		scores[a.ID] = fresh
		return fresh
	}
	bumpTop := func(t TopArtist, delta float64, rank int) {
		acc := bump(t.ArtistRef, delta)
		if acc == nil {
			return
		}
		for _, g := range t.Genres {
			acc.genres[g] = struct{}{}
		}
		if acc.sig.TopRank == 0 || rank < acc.sig.TopRank {
			acc.sig.TopRank = rank
		}
	}

	for _, a := range s.Followed {
		if acc := bump(a, weightFollowed); acc != nil {
			acc.sig.Followed = true
		}
	}
	for tr, list := range map[TimeRange][]TopArtist{
		ShortTerm:  s.Top.Short,
		MediumTerm: s.Top.Medium,
		LongTerm:   s.Top.Long,
	} {
		w := weightTop * timeRangeWeights[tr]
		for i, a := range list {
			// Best position across the three ranges. Taking the minimum
			// rather than the first one seen keeps this independent of the
			// map iteration order above, which Go randomises.
			bumpTop(a, w, i+1)
		}
	}
	for _, sa := range s.SavedAlbums {
		for _, a := range sa.Album.Artists {
			if acc := bump(a, weightSavedAlbum); acc != nil {
				acc.sig.SavedAlbums++
			}
		}
	}
	for _, st := range s.SavedTracks {
		for _, a := range st.Track.Artists {
			if acc := bump(a, weightSavedTrack); acc != nil {
				acc.sig.SavedTracks++
			}
		}
	}
	for _, rp := range s.Recent {
		for _, a := range rp.Track.Artists {
			if acc := bump(a, weightRecent); acc != nil {
				acc.sig.RecentPlays++
			}
		}
	}
	for _, pl := range s.PlaylistItems {
		// One playlist counts once for an artist however many of their
		// tracks are on it: "in 3 of your playlists" is a statement about
		// playlists, and counting tracks would make a single mix look like
		// three.
		inThis := map[string]struct{}{}
		for _, it := range pl {
			if it.Track == nil {
				continue
			}
			for _, a := range it.Track.Artists {
				acc := bump(a, weightPlaylist)
				if acc == nil {
					continue
				}
				if _, seen := inThis[a.ID]; !seen {
					inThis[a.ID] = struct{}{}
					acc.sig.Playlists++
				}
			}
		}
	}

	out := make([]ScoredArtist, 0, len(scores))
	for id, a := range scores {
		var genres []string
		if len(a.genres) > 0 {
			genres = make([]string, 0, len(a.genres))
			for g := range a.genres {
				genres = append(genres, g)
			}
			sort.Strings(genres)
		}
		out = append(out, ScoredArtist{ID: id, Name: a.name, Score: a.score, Genres: genres, Signals: a.sig})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > MaxScoredArtists {
		out = out[:MaxScoredArtists]
	}
	return out
}

// HydrateSources fans out to all six affinity endpoints in parallel and, for
// playlists, follows up with per-playlist item fetches for playlists the user
// owns or collaborates on (design §4.1 Feb 2026 change).
//
// A failing source degrades the profile; it does not destroy it. This used to
// run on an errgroup where any single error aborted the other five and
// returned nothing, so one Spotify 500 — or one source running past the
// deadline — meant no profile, which meant ScanConcertsWorker returned an
// error before writing any snapshot, which meant the user saw an empty feed
// and a spinner. Five sources' worth of signal is a fine profile; none is not
// a profile at all. Errors are collected and reported to the caller for
// logging, and only a total wipeout is an error.
//
// Note this is deliberately NOT errgroup.WithContext: a shared cancelling
// context is precisely the "one failure kills the rest" behavior being
// removed here.
func (c *Client) HydrateSources(ctx context.Context, accessToken, spotifyUserID string) (Sources, []error) {
	var (
		s    Sources
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []error
		ok   int
	)
	run := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := fn()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			ok++
		}()
	}

	run("recently_played", func() error {
		v, err := c.RecentlyPlayed(ctx, accessToken)
		s.Recent = v
		return err
	})
	run("top_artists", func() error {
		v, err := c.TopArtists(ctx, accessToken)
		s.Top = v
		return err
	})
	run("saved_tracks", func() error {
		v, err := c.SavedTracks(ctx, accessToken)
		s.SavedTracks = v
		return err
	})
	run("saved_albums", func() error {
		v, err := c.SavedAlbums(ctx, accessToken)
		s.SavedAlbums = v
		return err
	})
	run("followed_artists", func() error {
		v, err := c.FollowedArtists(ctx, accessToken)
		s.Followed = v
		return err
	})
	run("playlists", func() error {
		pls, err := c.UserPlaylists(ctx, accessToken)
		if err != nil {
			return err
		}
		// Filter to playlists we can actually read items from.
		mine := make([]Playlist, 0, len(pls))
		for _, p := range pls {
			if p.Owner.ID == spotifyUserID || p.Collaborative {
				mine = append(mine, p)
			}
		}
		if len(mine) > maxOwnedPlaylists {
			mine = mine[:maxOwnedPlaylists]
		}
		items, err := c.fetchPlaylistItemsBounded(ctx, accessToken, mine, 5)
		s.PlaylistItems = items
		return err
	})

	wg.Wait()
	if ok == 0 {
		return Sources{}, errs
	}
	return s, errs
}

// fetchPlaylistItemsBounded fetches items for each playlist with a bounded
// concurrency of `parallel`. Order in the returned slice matches `pls`.
//
// One unreadable playlist is skipped rather than failing the batch: a
// playlist can be deleted between the listing call and this one, and losing
// every other playlist's contribution over it is a poor trade at a weight of
// 0.2. The scorer already tolerates nil entries.
func (c *Client) fetchPlaylistItemsBounded(ctx context.Context, accessToken string, pls []Playlist, parallel int) ([][]PlaylistItem, error) {
	if parallel < 1 {
		parallel = 1
	}
	out := make([][]PlaylistItem, len(pls))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, p := range pls {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			items, err := c.PlaylistItems(ctx, accessToken, p.ID)
			if err != nil {
				slog.Debug("playlist items fetch failed", "playlist", p.ID, "err", err)
				return
			}
			out[i] = items
		}()
	}
	wg.Wait()
	return out, nil
}
