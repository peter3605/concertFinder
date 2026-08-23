package http

import (
	"testing"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/jobs"
)

// Two browser geolocation fixes taken from the same desk minutes apart, as
// observed in production. Before rounding they produced two location_keys, two
// snapshots, and two full cold scans about 10 metres apart.
const (
	fixA_lat, fixA_lng = 38.83352982119462, -77.2102163442201
	fixB_lat, fixB_lng = 38.83334891002311, -77.21031204910278
)

func TestJitteredFixesCollapseToOneLocationKey(t *testing.T) {
	a := concerts.Location{
		Latitude:    roundCoord(fixA_lat),
		Longitude:   roundCoord(fixA_lng),
		RadiusMiles: 50,
	}
	b := concerts.Location{
		Latitude:    roundCoord(fixB_lat),
		Longitude:   roundCoord(fixB_lng),
		RadiusMiles: 50,
	}

	if jobs.LocationKey(a) != jobs.LocationKey(b) {
		t.Fatalf("two fixes ~10m apart must share one snapshot key, got %q and %q\n"+
			"each distinct key costs a full cold scan (~400 of a 500/day TM cap)",
			jobs.LocationKey(a), jobs.LocationKey(b))
	}
}

// Rounding must not be so coarse that genuinely different places merge. A
// user moving between cities has to get their own snapshot.
func TestDistinctPlacesKeepDistinctKeys(t *testing.T) {
	dc := concerts.Location{Latitude: roundCoord(38.8951), Longitude: roundCoord(-77.0364), RadiusMiles: 50}
	nova := concerts.Location{Latitude: roundCoord(38.8335), Longitude: roundCoord(-77.2102), RadiusMiles: 50}

	if jobs.LocationKey(dc) == jobs.LocationKey(nova) {
		t.Fatalf("DC and Northern Virginia must not collapse to one key, both got %q", jobs.LocationKey(dc))
	}
}

// The radius is part of the key, so changing it is still a different search
// even from an identical point. Rounding must not disturb that.
func TestRadiusStillSeparatesKeys(t *testing.T) {
	near := concerts.Location{Latitude: roundCoord(fixA_lat), Longitude: roundCoord(fixA_lng), RadiusMiles: 10}
	far := concerts.Location{Latitude: roundCoord(fixA_lat), Longitude: roundCoord(fixA_lng), RadiusMiles: 50}

	if jobs.LocationKey(near) == jobs.LocationKey(far) {
		t.Fatal("radius must remain part of the snapshot identity")
	}
}

func TestRoundCoordHandlesNegativesAndZero(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{38.83352982119462, 38.83},
		{-77.2102163442201, -77.21},
		{-77.21031204910278, -77.21},
		{0, 0},
		// A literal like 1.005 is not representable in binary — the nearest
		// float64 is just below it — so this rounds down rather than away from
		// zero, which is what a decimal reading of math.Round would predict.
		// Pinned as the real behaviour rather than the arithmetic-class answer.
		// It does not matter here: the buckets are ~1.1km wide and the jitter
		// being absorbed is ~10m, so which side of an exact boundary a value
		// lands on is never the difference between one snapshot and two.
		{1.005, 1.0},
		{-1.005, -1.0},
	}
	for _, c := range cases {
		if got := roundCoord(c.in); got != c.want {
			t.Errorf("roundCoord(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
