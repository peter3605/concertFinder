package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
func (c *Client) TopArtists(ctx context.Context, accessToken string) (TopArtistsByRange, error) {
	fetch := func(tr TimeRange) ([]ArtistRef, error) {
		u := fmt.Sprintf("%s/me/top/artists?time_range=%s&limit=50", APIBase, tr)
		body, err := c.doGETRetry(ctx, u, accessToken)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []ArtistRef `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode top artists (%s): %w", tr, err)
		}
		return page.Items, nil
	}
	var out TopArtistsByRange
	var err error
	if out.Short, err = fetch(ShortTerm); err != nil {
		return out, err
	}
	if out.Medium, err = fetch(MediumTerm); err != nil {
		return out, err
	}
	if out.Long, err = fetch(LongTerm); err != nil {
		return out, err
	}
	return out, nil
}

// SavedTracks paginates through the user's saved-tracks library.
func (c *Client) SavedTracks(ctx context.Context, accessToken string) ([]SavedTrack, error) {
	var all []SavedTrack
	next := APIBase + "/me/tracks?limit=50"
	for next != "" {
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

// SavedAlbums paginates through the user's saved-albums library.
func (c *Client) SavedAlbums(ctx context.Context, accessToken string) ([]SavedAlbum, error) {
	var all []SavedAlbum
	next := APIBase + "/me/albums?limit=50"
	for next != "" {
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

// FollowedArtists uses cursor-based pagination via ?after=.
func (c *Client) FollowedArtists(ctx context.Context, accessToken string) ([]ArtistRef, error) {
	var all []ArtistRef
	next := APIBase + "/me/following?type=artist&limit=50"
	for next != "" {
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

// UserPlaylists returns metadata for all playlists visible to the user.
// The caller must filter to owned or collaborated ones before calling
// PlaylistItems (design §4.1: Feb 2026 change locks /playlists/{id}/items
// to owned/collaborated playlists only).
func (c *Client) UserPlaylists(ctx context.Context, accessToken string) ([]Playlist, error) {
	var all []Playlist
	next := APIBase + "/me/playlists?limit=50"
	for next != "" {
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

// PlaylistItems paginates through one playlist's items. Non-track items
// (podcast episodes, local files) appear with a nil Track field.
func (c *Client) PlaylistItems(ctx context.Context, accessToken, playlistID string) ([]PlaylistItem, error) {
	var all []PlaylistItem
	next := fmt.Sprintf("%s/playlists/%s/items?limit=50", APIBase, url.PathEscape(playlistID))
	for next != "" {
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
