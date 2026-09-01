package http

import "math"

// Input bounds for the /me endpoints.
//
// These are all deliberately far above what a real client sends. The point is
// not to police normal use but to stop a single account turning an endpoint
// into unbounded storage, unbounded work, or unbounded memory — every one of
// which is silent until the bill or the queue says otherwise.
const (
	// maxRequestBody caps every hand-decoded JSON body. Four kilobytes is
	// roughly two orders of magnitude more than the largest of these
	// (a display name and a flag), and matches what devices.go and
	// auth/mobile.go already use — one number, so a new handler copies the
	// right thing.
	maxRequestBody = 4 << 10

	// maxDedupKeyLen bounds the save key. A real one is a 64-character
	// sha256 hex digest (see concerts.DedupKey); the slack is there so a
	// future key format is a schema decision rather than an outage.
	maxDedupKeyLen = 128

	// maxArtistIDLen bounds the subscribe path parameter. Spotify IDs are
	// 22 characters of base62.
	maxArtistIDLen = 64

	// maxDisplayNameLen bounds the cached artist name. Long enough for the
	// longest band name anyone has actually registered with Spotify, short
	// enough that it cannot be used as a kilobyte-per-row store.
	maxDisplayNameLen = 200

	// maxSearchQueryLen bounds ?q= on the Spotify search proxy. Spotify
	// itself truncates well before this; the cap is here so the request we
	// forward is bounded by us and not by them.
	maxSearchQueryLen = 100

	// maxSavedConcerts and maxSubscribedArtists are per-user ceilings. Both
	// are far past any plausible collection — the entire snapshot a scan can
	// produce is smaller than the save ceiling — and exist because a save is
	// one POST with a caller-supplied key, so without a ceiling one account
	// can write rows into a shared table until the 0.5 GB storage cap is the
	// thing that notices.
	maxSavedConcerts     = 1000
	maxSubscribedArtists = 500

	// maxDailyLocations is how many distinct location_keys one account may
	// open per UTC day.
	//
	// Each new key is a full five-minute scan job with its own quota
	// reservation, against five worker slots for the whole deployment, so
	// cycling coordinates starves everyone else's scans, digests and pushes
	// while every individual job looks legitimate. Ten is generous for a
	// person — home, work, a trip, and a few radius changes — and the day's
	// allowance is a set, so returning to a location already visited today is
	// free (see db.RecordLocationVisit).
	maxDailyLocations = 10
)

// validCoords reports whether a latitude/longitude pair names a real point on
// Earth.
//
// The range comparisons alone would reject NaN and ±Inf — every comparison
// against NaN is false, and an infinity fails a bound — but the two are tested
// by name because what they cost is not visible from the range check. A NaN
// latitude formats as "NaN" through jobs.LocationKey, which is a perfectly
// good map key and a perfectly good snapshot identity, so it enqueues a
// five-minute scan exactly like a real place would, and a caller can mint an
// unlimited number of distinct ones.
func validCoords(lat, lng float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lng) || math.IsInf(lat, 0) || math.IsInf(lng, 0) {
		return false
	}
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}
