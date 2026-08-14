package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"golang.org/x/sync/errgroup"
)

// Page caps for the paginated affinity sources.
//
// Every one of these endpoints will happily walk a library of arbitrary size
// 50 rows at a time, sequentially, inside affinity.ComputeTimeout. A user with
// 10k saved tracks is 200 round trips from that source alone — comfortably
// past the timeout on its own, which meant hydration failed, the profile was
// never computed, and (because ScanConcertsWorker computes affinity before it
// scans) the user's feed stayed empty forever while the SWR poll re-enqueued a
// scan that could not succeed. The size of someone's library was silently the
// thing that decided whether the product worked for them.
//
// The caps are generous relative to what the scoring can actually use: the
// output is MaxScoredArtists (200) artists, and saved tracks and playlist
// items are the two lowest weights in the formula (0.5 and 0.2). Spotify
// returns these newest-first, so a cap keeps the most recent — the signal that
// best reflects current taste — and drops the long tail that was mostly
// re-confirming artists already scored by a heavier source.
const (
	maxSavedTrackPages   = 20 // 1000 tracks
	maxSavedAlbumPages   = 10 // 500 albums
	maxFollowedPages     = 20 // 1000 followed artists
	maxPlaylistPages     = 4  // 200 playlists scanned for ownership
	maxPlaylistItemPages = 4  // 200 items per playlist
	maxOwnedPlaylists    = 50 // playlists we actually fetch items for
)

// RecentlyPlayed returns up to 50 recent plays (Spotify caps this endpoint).
func (c *Client) RecentlyPlayed(ctx context.Context, accessToken string) ([]RecentPlay, error) {
	body, err := c.doGETRetry(ctx, APIBase+"/me/player/recently-played?limit=50", accessToken)
	if err != nil {
		return nil, err
	}
	var page struct {
		Items []RecentPlay `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("decode recently-played: %w", err)
	}
	return page.Items, nil
}

// TopArtists fetches the caller's top artists across all three time ranges.
// Spotify caps this at 50 per range, so one request per range suffices.
//
// The three ranges are independent, so they run concurrently — every other
// affinity source is already fanned out by HydrateSources, and three serial
// round trips here were the longest pole left on that path.
func (c *Client) TopArtists(ctx context.Context, accessToken string) (TopArtistsByRange, error) {
	fetch := func(ctx context.Context, tr TimeRange) ([]TopArtist, error) {
		u := fmt.Sprintf("%s/me/top/artists?time_range=%s&limit=50", APIBase, tr)
		body, err := c.doGETRetry(ctx, u, accessToken)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []TopArtist `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode top artists (%s): %w", tr, err)
		}
		return page.Items, nil
	}
	var out TopArtistsByRange
	targets := []struct {
		tr  TimeRange
		dst *[]TopArtist
	}{
		{ShortTerm, &out.Short},
		{MediumTerm, &out.Medium},
		{LongTerm, &out.Long},
	}
	g, gctx := errgroup.WithContext(ctx)
	for _, t := range targets {
		t := t
		g.Go(func() error {
			items, err := fetch(gctx, t.tr)
			if err != nil {
				return err
			}
			*t.dst = items
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return TopArtistsByRange{}, err
	}
	return out, nil
}

// SavedTracks paginates through the user's saved-tracks library, newest
// first, up to maxSavedTrackPages.
func (c *Client) SavedTracks(ctx context.Context, accessToken string) ([]SavedTrack, error) {
	var all []SavedTrack
	next := APIBase + "/me/tracks?limit=50"
	for page := 0; next != "" && page < maxSavedTrackPages; page++ {
		body, err := c.doGETRetry(ctx, next, accessToken)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []SavedTrack `json:"items"`
			Next  string       `json:"next"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode saved tracks: %w", err)
		}
		all = append(all, page.Items...)
		next = page.Next
	}
	return all, nil
}

// SavedAlbums paginates through the user's saved-albums library, up to
// maxSavedAlbumPages.
func (c *Client) SavedAlbums(ctx context.Context, accessToken string) ([]SavedAlbum, error) {
	var all []SavedAlbum
	next := APIBase + "/me/albums?limit=50"
	for page := 0; next != "" && page < maxSavedAlbumPages; page++ {
		body, err := c.doGETRetry(ctx, next, accessToken)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []SavedAlbum `json:"items"`
			Next  string       `json:"next"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode saved albums: %w", err)
		}
		all = append(all, page.Items...)
		next = page.Next
	}
	return all, nil
}

// FollowedArtists uses cursor-based pagination via ?after=, up to
// maxFollowedPages.
func (c *Client) FollowedArtists(ctx context.Context, accessToken string) ([]ArtistRef, error) {
	var all []ArtistRef
	next := APIBase + "/me/following?type=artist&limit=50"
	for page := 0; next != "" && page < maxFollowedPages; page++ {
		body, err := c.doGETRetry(ctx, next, accessToken)
		if err != nil {
			return nil, err
		}
		var page struct {
			Artists struct {
				Items []ArtistRef `json:"items"`
				Next  string      `json:"next"`
			} `json:"artists"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode followed artists: %w", err)
		}
		all = append(all, page.Artists.Items...)
		next = page.Artists.Next
	}
	return all, nil
}

// UserPlaylists returns metadata for playlists visible to the user, up to
// maxPlaylistPages. The caller must filter to owned or collaborated ones
// before calling PlaylistItems (design §4.1: Feb 2026 change locks
// /playlists/{id}/items to owned/collaborated playlists only).
func (c *Client) UserPlaylists(ctx context.Context, accessToken string) ([]Playlist, error) {
	var all []Playlist
	next := APIBase + "/me/playlists?limit=50"
	for page := 0; next != "" && page < maxPlaylistPages; page++ {
		body, err := c.doGETRetry(ctx, next, accessToken)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []Playlist `json:"items"`
			Next  string     `json:"next"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode playlists: %w", err)
		}
		all = append(all, page.Items...)
		next = page.Next
	}
	return all, nil
}

// PlaylistItems paginates through one playlist's items, up to
// maxPlaylistItemPages. Non-track items (podcast episodes, local files)
// appear with a nil Track field.
func (c *Client) PlaylistItems(ctx context.Context, accessToken, playlistID string) ([]PlaylistItem, error) {
	var all []PlaylistItem
	next := fmt.Sprintf("%s/playlists/%s/items?limit=50", APIBase, url.PathEscape(playlistID))
	for page := 0; next != "" && page < maxPlaylistItemPages; page++ {
		body, err := c.doGETRetry(ctx, next, accessToken)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []PlaylistItem `json:"items"`
			Next  string         `json:"next"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode playlist items: %w", err)
		}
		all = append(all, page.Items...)
		next = page.Next
	}
	return all, nil
}
