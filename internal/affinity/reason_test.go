package affinity

import (
	"testing"

	"github.com/peterho/concertfinder/internal/spotify"
)

func TestReasonNamesTheStrongestSignal(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  spotify.ArtistSignals
		want string
	}{
		{
			// Every signal at once. Following outweighs the rest by §4.3's
			// own weights, so it is the one that gets the line.
			name: "followed outranks everything",
			sig: spotify.ArtistSignals{
				Followed: true, TopRank: 3, SavedAlbums: 4,
				SavedTracks: 20, RecentPlays: 9, Playlists: 2,
			},
			want: "You follow them",
		},
		{
			name: "top rank outranks saves",
			sig:  spotify.ArtistSignals{TopRank: 7, SavedAlbums: 4, SavedTracks: 20},
			want: "#7 in your top artists",
		},
		{
			name: "albums outrank tracks",
			sig:  spotify.ArtistSignals{SavedAlbums: 3, SavedTracks: 20, RecentPlays: 4},
			want: "You saved 3 of their albums",
		},
		{name: "one album reads as a word", sig: spotify.ArtistSignals{SavedAlbums: 1}, want: "You saved one of their albums"},
		{name: "tracks outrank plays", sig: spotify.ArtistSignals{SavedTracks: 5, RecentPlays: 2}, want: "You saved 5 of their tracks"},
		{name: "one track", sig: spotify.ArtistSignals{SavedTracks: 1}, want: "You saved one of their tracks"},
		{
			// The count is a window of the last 50 plays, not a lifetime
			// total, so the number would be misleading. The fact is not.
			name: "recent plays are not counted out loud",
			sig:  spotify.ArtistSignals{RecentPlays: 9},
			want: "Recently played",
		},
		{name: "one playlist", sig: spotify.ArtistSignals{Playlists: 1}, want: "In one of your playlists"},
		{name: "several playlists", sig: spotify.ArtistSignals{Playlists: 3}, want: "In 3 of your playlists"},
		{
			// Past the ceiling the rank is a fact about our top-200 cap
			// rather than about the user, so it loses the number.
			name: "a deep rank drops the number",
			sig:  spotify.ArtistSignals{TopRank: TopRankCeiling + 1},
			want: "One of your top artists",
		},
		{
			// A profile computed before signals were recorded. Both clients
			// render nothing at all rather than a placeholder — inventing
			// one would be the app claiming to know something it doesn't.
			name: "no signal, no claim",
			sig:  spotify.ArtistSignals{},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Reason(tc.sig); got != tc.want {
				t.Errorf("Reason(%+v) = %q, want %q", tc.sig, got, tc.want)
			}
		})
	}
}

func TestReasonsSkipsArtistsWithNothingToSay(t *testing.T) {
	got := Reasons([]spotify.ScoredArtist{
		{ID: "a", Signals: spotify.ArtistSignals{Followed: true}},
		{ID: "b"},
	})
	if got["a"] != "You follow them" {
		t.Errorf("a: got %q", got["a"])
	}
	// A miss and an empty reason have to be the same thing to the caller,
	// which is what lets the handler write `if reason, ok := ...` without a
	// second emptiness check.
	if _, present := got["b"]; present {
		t.Errorf("b has no signals and should not be in the map: %q", got["b"])
	}
}
