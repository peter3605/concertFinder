package spotify

// ArtistRef is the minimal artist identity carried through affinity computation.
type ArtistRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TrackRef holds only what affinity scoring needs from a track.
type TrackRef struct {
	ID      string      `json:"id"`
	Artists []ArtistRef `json:"artists"`
	// Type distinguishes tracks from podcast episodes in playlist items.
	Type string `json:"type,omitempty"`
}

type AlbumRef struct {
	ID      string      `json:"id"`
	Artists []ArtistRef `json:"artists"`
}

type SavedTrack struct {
	Track TrackRef `json:"track"`
}

type SavedAlbum struct {
	Album AlbumRef `json:"album"`
}

type RecentPlay struct {
	Track TrackRef `json:"track"`
}

type Playlist struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Collaborative bool   `json:"collaborative"`
	Owner         struct {
		ID string `json:"id"`
	} `json:"owner"`
}

// PlaylistItem.Track is nil for local files or podcast episodes.
type PlaylistItem struct {
	Track *TrackRef `json:"track"`
}

// TimeRange for /me/top/{artists,tracks}.
type TimeRange string

const (
	ShortTerm  TimeRange = "short_term"
	MediumTerm TimeRange = "medium_term"
	LongTerm   TimeRange = "long_term"
)

// TopArtistsByRange is what the scorer wants: three lists of top artists.
type TopArtistsByRange struct {
	Short  []ArtistRef
	Medium []ArtistRef
	Long   []ArtistRef
}
