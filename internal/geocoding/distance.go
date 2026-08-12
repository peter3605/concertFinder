package geocoding

import "math"

// EarthRadiusMiles is the mean radius used by HaversineMiles.
const EarthRadiusMiles = 3958.8

// HaversineMiles is the great-circle distance between two lat/lng points.
//
// This lived in internal/bandsintown until that source was removed, with a
// second unexported copy in internal/concerts. Two implementations of one
// formula is one too many: a radius filter that disagrees with the radius
// filter applied upstream of it silently drops or admits shows at the edge
// of a user's circle.
func HaversineMiles(lat1, lng1, lat2, lng2 float64) float64 {
	const rad = math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return EarthRadiusMiles * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
