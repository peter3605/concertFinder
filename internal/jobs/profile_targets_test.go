package jobs

import (
	"slices"
	"testing"

	"github.com/peterho/concertfinder/internal/spotify"
)

// Profile artists must reach concerts.Search exactly as they did before
// CF-29 moved the search off spotify.ScoredArtist: same ID (it keys
// artist_resolutions, the per-artist cache and the viewer's artist ref), same
// score (it gates the fallback), same genres, and no attraction ID, so they
// still resolve by name.
func TestProfileTargets_CarriesEveryFieldSearchReads(t *testing.T) {
	in := []spotify.ScoredArtist{
		{ID: "sp1", Name: "Alpha", Score: 2.5, Genres: []string{"indie rock"}},
		{ID: "sp2", Name: "Beta", Score: 0.4},
	}
	got := profileTargets(in)
	if len(got) != len(in) {
		t.Fatalf("got %d targets, want %d", len(got), len(in))
	}
	for i, a := range in {
		g := got[i]
		if g.ID != a.ID || g.Name != a.Name || g.Score != a.Score || !slices.Equal(g.Genres, a.Genres) {
			t.Errorf("target %d = %+v, want fields from %+v", i, g, a)
		}
		if g.AttractionID != "" {
			t.Errorf("target %d has attraction %q; profile artists must resolve by name", i, g.AttractionID)
		}
	}
}
