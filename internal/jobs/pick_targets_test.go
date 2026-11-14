package jobs

import (
	"testing"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
)

// A pick already holds its attraction ID, so it reserves one permit (the
// events call) where a profile artist reserves two (resolution plus events):
// docs/public-path.md §2.1. Reserving 2N for picks would hold double their
// cost against the user's cap and the shared account ceiling for the whole
// scan.
func TestScanQuotaFor(t *testing.T) {
	picks := pickTargets([]db.ArtistPick{{AttractionID: "a", Name: "A"}, {AttractionID: "b", Name: "B"}, {AttractionID: "c", Name: "C"}})
	profile := []concerts.Target{{ID: "sp1", Name: "X"}, {ID: "sp2", Name: "Y"}}
	for _, tc := range []struct {
		name    string
		targets []concerts.Target
		want    int
	}{
		{"picks only", picks, 3},
		{"profile only", profile, 4},
		{"both", append(append([]concerts.Target{}, picks...), profile...), 7},
		{"nothing", nil, 0},
	} {
		if got := scanQuotaFor(tc.targets); got != tc.want {
			t.Errorf("%s: scanQuotaFor = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A pick is searched by its attraction ID under a tm: identity, carrying
// Ticketmaster's genre onto the Target, because tmEventToConcert copies
// Target.ID and Target.Genres onto every concert and from there into the
// viewer's artist refs. An empty ID would leave the bell off and
// instant-notify unmatched, silently.
func TestPickTargets(t *testing.T) {
	got := pickTargets([]db.ArtistPick{
		{AttractionID: "K8vGEESE", Name: "Geese", Genre: "Rock"},
		{AttractionID: "K8vNOGENRE", Name: "No Genre"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d targets", len(got))
	}
	g := got[0]
	if g.ID != "tm:K8vGEESE" || g.AttractionID != "K8vGEESE" || g.Name != "Geese" || len(g.Genres) != 1 || g.Genres[0] != "Rock" {
		t.Errorf("target = %+v", g)
	}
	if got[1].Genres != nil {
		t.Errorf("a pick with no genre got genres %v", got[1].Genres)
	}
}

// Picks go first, a profile artist the user also picked is dropped from the
// profile half (one call instead of two for the same shows), and the total
// stops at the limit.
func TestMergeTargets(t *testing.T) {
	picks := pickTargets([]db.ArtistPick{{AttractionID: "K8vB", Name: "Beyoncé"}, {AttractionID: "K8vG", Name: "Geese"}})
	profile := []concerts.Target{
		{ID: "sp1", Name: "Alpha"},
		{ID: "sp2", Name: "Beyonce"}, // same artist as a pick, under folding
		{ID: "sp3", Name: "Gamma"},
		{ID: "sp4", Name: "Delta"},
	}
	got := mergeTargets(picks, profile, 4)
	want := []string{"tm:K8vB", "tm:K8vG", "sp1", "sp3"}
	if len(got) != len(want) {
		t.Fatalf("got %d targets, want %d: %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("target %d = %s, want %s", i, got[i].ID, id)
		}
	}
	if n := len(mergeTargets(picks, profile, 1)); n != 1 {
		t.Errorf("limit 1 gave %d targets", n)
	}
}
