package http

import (
	"math"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/jobs"
)

// radius_miles has been bounded 1..500 since the endpoint shipped and the
// coordinates were not, which is backwards. A coordinate is half of a
// snapshot's identity, and a scan is keyed by that identity — so an
// unconstrained pair is an unconstrained supply of five-minute jobs against
// five worker slots for the whole deployment.
func TestValidCoords(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lat, lng float64
		want     bool
	}{
		{"a real place", 38.89, -77.03, true},
		{"the poles", 90, 180, true},
		{"the other corner", -90, -180, true},
		{"null island", 0, 0, true},
		{"latitude past the pole", 91, 0, false},
		{"longitude past the antimeridian", 0, 181, false},
		{"latitude below the pole", -90.0001, 0, false},
		{"longitude below the antimeridian", 0, -180.0001, false},
		{"NaN latitude", math.NaN(), 0, false},
		{"NaN longitude", 0, math.NaN(), false},
		{"+Inf latitude", math.Inf(1), 0, false},
		{"-Inf longitude", 0, math.Inf(-1), false},
	} {
		if got := validCoords(tc.lat, tc.lng); got != tc.want {
			t.Errorf("%s: validCoords(%v, %v) = %v, want %v", tc.name, tc.lat, tc.lng, got, tc.want)
		}
	}
}

// The specific reason NaN is called out rather than left to the range check:
// it formats into a perfectly ordinary location_key, so it buys a snapshot and
// a scan job exactly like a real place would, and a caller can mint an
// unlimited supply of them by varying the radius.
func TestNonFiniteCoordsWouldOtherwiseMakeValidLookingSnapshotKeys(t *testing.T) {
	bad := concerts.Location{Latitude: math.NaN(), Longitude: math.Inf(1), RadiusMiles: 50}
	key := jobs.LocationKey(bad)
	if key == "" {
		t.Fatal("test premise broken: LocationKey now rejects non-finite input itself")
	}
	if validCoords(bad.Latitude, bad.Longitude) {
		t.Fatalf("a location that formats to the perfectly usable key %q must not pass validation", key)
	}
}

// Retry-After on the daily-location refusal points at the next UTC midnight,
// the same boundary the upstream quota ledger rolls on. It must never be zero:
// a client told to wait no time at all retries into the same refusal.
func TestSecondsUntilNextUTCDay(t *testing.T) {
	justAfterMidnight := time.Date(2026, 3, 4, 0, 0, 1, 0, time.UTC)
	if got := secondsUntilNextUTCDay(justAfterMidnight); got != 24*3600-1 {
		t.Errorf("just after midnight = %d, want %d", got, 24*3600-1)
	}
	justBeforeMidnight := time.Date(2026, 3, 4, 23, 59, 59, 0, time.UTC)
	if got := secondsUntilNextUTCDay(justBeforeMidnight); got != 1 {
		t.Errorf("just before midnight = %d, want 1", got)
	}
	if got := secondsUntilNextUTCDay(time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)); got <= 0 {
		t.Errorf("exactly midnight = %d, want a positive number of seconds", got)
	}
}

// The ceilings are policy, and the policy is "far past any plausible
// collection, present only so one account cannot write into a shared table
// without bound". Pinned so a later edit that makes one of them small enough
// to hit in normal use has to say so out loud.
func TestPerUserCeilingsStayGenerous(t *testing.T) {
	if maxSavedConcerts < 500 {
		t.Errorf("maxSavedConcerts = %d; a real collector would hit this", maxSavedConcerts)
	}
	if maxSubscribedArtists < 200 {
		t.Errorf("maxSubscribedArtists = %d; below the artist count of a single scan", maxSubscribedArtists)
	}
	if maxDailyLocations < 5 {
		t.Errorf("maxDailyLocations = %d; home, work and a trip in one day is ordinary", maxDailyLocations)
	}
	// A dedup_key is a 64-character sha256 hex digest; the bound has to clear
	// it or every save fails.
	if maxDedupKeyLen < 64 {
		t.Errorf("maxDedupKeyLen = %d, below the length of a real dedup_key", maxDedupKeyLen)
	}
	// Spotify artist IDs are 22 characters of base62.
	if maxArtistIDLen < 22 {
		t.Errorf("maxArtistIDLen = %d, below the length of a real Spotify artist ID", maxArtistIDLen)
	}
}
