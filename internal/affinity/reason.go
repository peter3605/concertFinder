package affinity

import (
	"fmt"

	"github.com/peterho/concertfinder/internal/spotify"
)

// TopRankCeiling is the worst position that is still worth naming. "#4 in
// your top artists" is a fact about the user; "#187" is a fact about our cap,
// and reads as filler. Past it the next-strongest signal speaks instead.
const TopRankCeiling = 50

// Reason renders one short line explaining why an artist is in the feed,
// from the profile's own per-signal breakdown. Empty means "we have nothing
// honest to say" — a profile computed before ArtistSignals existed, or an
// artist whose every contribution was a signal we don't have wording for —
// and both clients render nothing at all in that case rather than a
// placeholder.
//
// Exactly one signal is named, and it is the strongest *weighted* one, in the
// order §4.3 weights them: followed (1.0), top artists (0.9), saved albums
// (0.7), saved tracks (0.5), recently played (0.4), playlists (0.2). Listing
// several would be more complete and less useful — the card has one line, and
// the point is to answer "why is this here?" at a glance.
//
// Nothing here names a track, an album, or a playlist. The counts come from
// the derived profile; no raw Spotify Content reaches this function, and
// none is needed to say something true.
func Reason(s spotify.ArtistSignals) string {
	switch {
	case s.Followed:
		return "You follow them"
	case s.TopRank > 0 && s.TopRank <= TopRankCeiling:
		return fmt.Sprintf("#%d in your top artists", s.TopRank)
	case s.SavedAlbums == 1:
		return "You saved one of their albums"
	case s.SavedAlbums > 1:
		return fmt.Sprintf("You saved %d of their albums", s.SavedAlbums)
	case s.SavedTracks == 1:
		return "You saved one of their tracks"
	case s.SavedTracks > 1:
		return fmt.Sprintf("You saved %d of their tracks", s.SavedTracks)
	case s.RecentPlays > 0:
		return "Recently played"
	case s.Playlists == 1:
		return "In one of your playlists"
	case s.Playlists > 1:
		return fmt.Sprintf("In %d of your playlists", s.Playlists)
	// A top-artist rank past the ceiling with no other signal: still true,
	// still the only thing we know, so say the unranked half of it rather
	// than nothing.
	case s.TopRank > 0:
		return "One of your top artists"
	}
	return ""
}

// Reasons maps artist ID to Reason for a whole profile, skipping artists
// with nothing to say so a lookup miss and an empty reason are the same
// thing to the caller.
func Reasons(artists []spotify.ScoredArtist) map[string]string {
	out := make(map[string]string, len(artists))
	for _, a := range artists {
		if r := Reason(a.Signals); r != "" {
			out[a.ID] = r
		}
	}
	return out
}
