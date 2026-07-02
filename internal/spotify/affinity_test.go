package spotify

import (
	"math"
	"testing"
)

func TestScoreArtists_FormulaMatchesDesign(t *testing.T) {
	// Artist A: followed, top-short + top-long, on 2 saved albums, 1 saved track,
	//           1 recent play, appears in 3 playlist items.
	// Artist B: only top-medium.
	// Artist C: only 1 saved track.
	a := ArtistRef{ID: "a", Name: "Alpha"}
	b := ArtistRef{ID: "b", Name: "Beta"}
	c := ArtistRef{ID: "c", Name: "Gamma"}

	src := Sources{
		Followed: []ArtistRef{a},
		Top: TopArtistsByRange{
			Short:  []TopArtist{{ArtistRef: a, Genres: []string{"rock", "art rock"}}},
			Medium: []TopArtist{{ArtistRef: b}},
			Long:   []TopArtist{{ArtistRef: a, Genres: []string{"rock"}}},
		},
		SavedAlbums: []SavedAlbum{
			{Album: AlbumRef{Artists: []ArtistRef{a}}},
			{Album: AlbumRef{Artists: []ArtistRef{a}}},
		},
		SavedTracks: []SavedTrack{
			{Track: TrackRef{Artists: []ArtistRef{a}}},
			{Track: TrackRef{Artists: []ArtistRef{c}}},
		},
		Recent: []RecentPlay{
			{Track: TrackRef{Artists: []ArtistRef{a}}},
		},
		PlaylistItems: [][]PlaylistItem{
			{
				{Track: &TrackRef{Artists: []ArtistRef{a}}},
				{Track: &TrackRef{Artists: []ArtistRef{a}}},
				{Track: &TrackRef{Artists: []ArtistRef{a}}},
				{Track: nil}, // podcast episode — must be skipped
			},
		},
	}

	got := ScoreArtists(src)
	scores := map[string]float64{}
	for _, s := range got {
		scores[s.ID] = s.Score
	}

	// A: 1.0 + 0.9*(1.0+0.6) + 0.7*2 + 0.5*1 + 0.4*1 + 0.2*3
	//  = 1.0 + 1.44 + 1.4 + 0.5 + 0.4 + 0.6 = 5.34
	// B: 0.9 * 0.8 = 0.72
	// C: 0.5
	wantA := 5.34
	wantB := 0.72
	wantC := 0.5

	if !approxEq(scores["a"], wantA) {
		t.Errorf("A: got %v, want %v", scores["a"], wantA)
	}
	if !approxEq(scores["b"], wantB) {
		t.Errorf("B: got %v, want %v", scores["b"], wantB)
	}
	if !approxEq(scores["c"], wantC) {
		t.Errorf("C: got %v, want %v", scores["c"], wantC)
	}

	if got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Errorf("order wrong: %+v", got)
	}
}

func TestScoreArtists_CapsAtMax(t *testing.T) {
	// Build MaxScoredArtists+5 distinct artists, all followed.
	src := Sources{}
	n := MaxScoredArtists + 5
	src.Followed = make([]ArtistRef, n)
	for i := 0; i < n; i++ {
		src.Followed[i] = ArtistRef{ID: string(rune('a' + i%26)) + string(rune('0'+i/26)), Name: "x"}
	}
	got := ScoreArtists(src)
	if len(got) != MaxScoredArtists {
		t.Fatalf("expected %d, got %d", MaxScoredArtists, len(got))
	}
}

func TestScoreArtists_TieBreakByName(t *testing.T) {
	src := Sources{
		Followed: []ArtistRef{
			{ID: "1", Name: "Zed"},
			{ID: "2", Name: "Aaron"},
			{ID: "3", Name: "Mary"},
		},
	}
	got := ScoreArtists(src)
	if got[0].Name != "Aaron" || got[1].Name != "Mary" || got[2].Name != "Zed" {
		t.Errorf("tie-break order wrong: %+v", got)
	}
}

func TestScoreArtists_UnionsGenresAcrossRanges(t *testing.T) {
	a := ArtistRef{ID: "a", Name: "A"}
	src := Sources{
		Top: TopArtistsByRange{
			Short:  []TopArtist{{ArtistRef: a, Genres: []string{"rock", "art rock"}}},
			Medium: []TopArtist{{ArtistRef: a, Genres: []string{"rock", "post-rock"}}},
			Long:   []TopArtist{{ArtistRef: a}},
		},
	}
	got := ScoreArtists(src)
	if len(got) != 1 {
		t.Fatalf("wrong len: %+v", got)
	}
	// Union should be sorted, dedup'd.
	want := []string{"art rock", "post-rock", "rock"}
	if len(got[0].Genres) != len(want) {
		t.Fatalf("genres wrong: %+v", got[0].Genres)
	}
	for i, g := range want {
		if got[0].Genres[i] != g {
			t.Errorf("genres[%d]: got %q, want %q", i, got[0].Genres[i], g)
		}
	}
}

func TestScoreArtists_SkipsEmptyID(t *testing.T) {
	src := Sources{
		Followed: []ArtistRef{{ID: "", Name: "Unknown"}, {ID: "x", Name: "X"}},
	}
	got := ScoreArtists(src)
	if len(got) != 1 || got[0].ID != "x" {
		t.Errorf("empty-ID artist not dropped: %+v", got)
	}
}

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
