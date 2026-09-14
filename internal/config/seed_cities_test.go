package config

import "testing"

// A malformed entry must be an error, not a skip. Dropping one city quietly
// leaves that market's landing page empty, which is indistinguishable from the
// cache simply being cold — the exact silence the seed exists to remove.
func TestParseSeedCities(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "unset yields the defaults", raw: "", want: len(defaultSeedCities)},
		{name: "none disables the seed", raw: "none", want: 0},
		{name: "none is case-insensitive", raw: "NONE", want: 0},
		{name: "one city", raw: "New York:40.7128,-74.0060", want: 1},
		{name: "several cities", raw: "New York:40.7128,-74.0060;Chicago:41.8781,-87.6298", want: 2},
		{name: "whitespace is tolerated", raw: " New York : 40.7128 , -74.0060 ", want: 1},
		{name: "missing coordinates", raw: "New York", wantErr: true},
		{name: "missing longitude", raw: "New York:40.7128", wantErr: true},
		{name: "unparseable latitude", raw: "New York:north,-74.0060", wantErr: true},
		{name: "latitude off the planet", raw: "Nowhere:91.0,-74.0060", wantErr: true},
		{name: "longitude off the planet", raw: "Nowhere:40.7128,-181.0", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSeedCities(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSeedCities(%q) = %v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSeedCities(%q): %v", tc.raw, err)
			}
			if len(got) != tc.want {
				t.Fatalf("parseSeedCities(%q) returned %d cities, want %d", tc.raw, len(got), tc.want)
			}
		})
	}
}

// New York is the coordinate the signed-out web page is hardcoded to, so it is
// the one city where an empty cache is guaranteed to be the thing a stranger
// sees. It has to be in the default list.
func TestDefaultSeedCitiesCoverTheLandingPageCoordinate(t *testing.T) {
	// web/src/pages/discover.tsx DEFAULT_LAT / DEFAULT_LNG.
	const wantLat, wantLng = 40.7128, -74.006
	for _, c := range defaultSeedCities {
		if withinAMile(c.Latitude, c.Longitude, wantLat, wantLng) {
			return
		}
	}
	t.Fatalf("no default seed city near the landing page's hardcoded coordinate (%v, %v)", wantLat, wantLng)
}

// withinAMile is a crude degree-space comparison: at these latitudes 0.02
// degrees is comfortably inside the seed's 50-mile radius, and the point is to
// catch a city being dropped, not to measure one.
func withinAMile(lat, lng, wantLat, wantLng float64) bool {
	d := func(a, b float64) float64 {
		if a > b {
			return a - b
		}
		return b - a
	}
	return d(lat, wantLat) < 0.02 && d(lng, wantLng) < 0.02
}

// A malformed list must stop the server rather than start it with a silently
// shorter one. Validate is where Load's parse failure surfaces.
func TestValidateRejectsMalformedSeedCities(t *testing.T) {
	_, err := parseSeedCities("New York")
	if err == nil {
		t.Fatal("expected a parse error to carry into Validate")
	}
	c := Config{discoverSeedCitiesErr: err}
	for _, e := range c.Validate() {
		if e == err {
			return
		}
	}
	t.Fatal("Validate did not report the DISCOVER_SEED_CITIES parse failure")
}
