package spotify

import (
	"context"
	"sort"

	"golang.org/x/sync/errgroup"
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
	bumpTop := func(t TopArtist, delta float64) {
		acc := bump(t.ArtistRef, delta)
		if acc == nil {
			return
		}
		for _, g := range t.Genres {
			acc.genres[g] = struct{}{}
		}
	}

	for _, a := range s.Followed {
		bump(a, weightFollowed)
	}
	for tr, list := range map[TimeRange][]TopArtist{
		ShortTerm:  s.Top.Short,
		MediumTerm: s.Top.Medium,
		LongTerm:   s.Top.Long,
	} {
		w := weightTop * timeRangeWeights[tr]
		for _, a := range list {
			bumpTop(a, w)
		}
	}
	for _, sa := range s.SavedAlbums {
		for _, a := range sa.Album.Artists {
			bump(a, weightSavedAlbum)
		}
	}
	for _, st := range s.SavedTracks {
		for _, a := range st.Track.Artists {
			bump(a, weightSavedTrack)
		}
	}
	for _, rp := range s.Recent {
		for _, a := range rp.Track.Artists {
			bump(a, weightRecent)
		}
	}
	for _, pl := range s.PlaylistItems {
		for _, it := range pl {
			if it.Track == nil {
				continue
			}
			for _, a := range it.Track.Artists {
				bump(a, weightPlaylist)
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
		out = append(out, ScoredArtist{ID: id, Name: a.name, Score: a.score, Genres: genres})
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
// owns or collaborates on (design §4.1 Feb 2026 change). Any endpoint error
// fails the whole hydration.
func (c *Client) HydrateSources(ctx context.Context, accessToken, spotifyUserID string) (Sources, error) {
	var s Sources
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		v, err := c.RecentlyPlayed(gctx, accessToken)
		if err != nil {
			return err
		}
		s.Recent = v
		return nil
	})
	g.Go(func() error {
		v, err := c.TopArtists(gctx, accessToken)
		if err != nil {
			return err
		}
		s.Top = v
		return nil
	})
	g.Go(func() error {
		v, err := c.SavedTracks(gctx, accessToken)
		if err != nil {
			return err
		}
		s.SavedTracks = v
		return nil
	})
	g.Go(func() error {
		v, err := c.SavedAlbums(gctx, accessToken)
		if err != nil {
			return err
		}
		s.SavedAlbums = v
		return nil
	})
	g.Go(func() error {
		v, err := c.FollowedArtists(gctx, accessToken)
		if err != nil {
			return err
		}
		s.Followed = v
		return nil
	})
	g.Go(func() error {
		pls, err := c.UserPlaylists(gctx, accessToken)
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
		items, err := c.fetchPlaylistItemsBounded(gctx, accessToken, mine, 5)
		if err != nil {
			return err
		}
		s.PlaylistItems = items
		return nil
	})

	if err := g.Wait(); err != nil {
		return Sources{}, err
	}
	return s, nil
}

// fetchPlaylistItemsBounded fetches items for each playlist with a bounded
// concurrency of `parallel`. Order in the returned slice matches `pls`.
func (c *Client) fetchPlaylistItemsBounded(ctx context.Context, accessToken string, pls []Playlist, parallel int) ([][]PlaylistItem, error) {
	if parallel < 1 {
		parallel = 1
	}
	out := make([][]PlaylistItem, len(pls))
	sem := make(chan struct{}, parallel)
	g, gctx := errgroup.WithContext(ctx)
	for i, p := range pls {
		i, p := i, p
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }()
			items, err := c.PlaylistItems(gctx, accessToken, p.ID)
			if err != nil {
				return err
			}
			out[i] = items
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
