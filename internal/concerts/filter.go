package concerts

import (
	"math"
	"strings"
	"time"
)

// Weekday enum for the "weekday/weekend" filter (design §10.2).
type Weekday string

const (
	WeekdayAll     Weekday = "all"
	WeekdayWeekday Weekday = "weekday"
	WeekdayWeekend Weekday = "weekend"
)

// Filters applied server-side after Search returns.
type Filters struct {
	Genre    string    // case-insensitive substring; empty = no filter
	DateFrom time.Time // zero = no lower bound
	DateTo   time.Time // zero = no upper bound
	Weekday  Weekday   // default WeekdayAll
	// RadiusMiles clips further than the user's saved radius. 0 = use the
	// saved radius (already applied at fetch time).
	RadiusMiles int
	Origin      Location // needed only if RadiusMiles > 0
}

// Apply returns a filtered slice preserving order.
func Apply(cs []Concert, f Filters) []Concert {
	if f.Weekday == "" {
		f.Weekday = WeekdayAll
	}
	genre := strings.ToLower(strings.TrimSpace(f.Genre))
	out := make([]Concert, 0, len(cs))
	for _, c := range cs {
		if !f.DateFrom.IsZero() && c.Date.Before(f.DateFrom) {
			continue
		}
		if !f.DateTo.IsZero() && c.Date.After(f.DateTo) {
			continue
		}
		if genre != "" && !matchesGenre(c.Artist.Genres, genre) {
			continue
		}
		if f.Weekday != WeekdayAll && !matchesWeekday(c.Date, f.Weekday) {
			continue
		}
		if f.RadiusMiles > 0 && c.Latitude != 0 && c.Longitude != 0 {
			if haversineMiles(f.Origin.Latitude, f.Origin.Longitude, c.Latitude, c.Longitude) > float64(f.RadiusMiles) {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func matchesGenre(genres []string, needle string) bool {
	for _, g := range genres {
		if strings.Contains(strings.ToLower(g), needle) {
			return true
		}
	}
	return false
}

func matchesWeekday(t time.Time, want Weekday) bool {
	d := t.Weekday()
	weekend := d == time.Friday || d == time.Saturday || d == time.Sunday
	switch want {
	case WeekdayWeekend:
		return weekend
	case WeekdayWeekday:
		return !weekend
	default:
		return true
	}
}

func haversineMiles(lat1, lng1, lat2, lng2 float64) float64 {
	const earthMiles = 3958.8
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthMiles * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
