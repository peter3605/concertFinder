package concerts

import (
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
	// Genre matches one of an artist's genre tags exactly, case-insensitively.
	// Empty = no filter. Exact rather than substring so the counts on the
	// facet pills mean what they say: with substring matching, clicking a
	// "rock · 12" pill also pulled in "indie rock" and "post-rock" and
	// returned forty results.
	Genre string
	// Venue matches a single venue, compared under Normalize. Empty = no
	// filter. Normalized rather than raw because sources disagree on
	// capitalization and punctuation for the same room — real data has
	// "9:30 CLUB" from one feed, and a second feed writing "9:30 Club"
	// would otherwise be a separate, separately-counted venue.
	Venue    string
	DateFrom time.Time // zero = no lower bound
	DateTo   time.Time // zero = no upper bound
	Weekday  Weekday   // default WeekdayAll
	// There is deliberately no radius field here. One existed, clipping
	// tighter than the user's saved radius, but no client ever sent the query
	// parameter that set it — the radius the user picks is applied upstream at
	// fetch time, against Ticketmaster and the fallback's haversine filter.
	// Unreachable filtering that looks reachable is worse than none.
}

// Apply returns a filtered slice preserving order.
func Apply(cs []Concert, f Filters) []Concert {
	if f.Weekday == "" {
		f.Weekday = WeekdayAll
	}
	genre := strings.ToLower(strings.TrimSpace(f.Genre))
	// Normalize once, not per concert.
	venue := Normalize(f.Venue)
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
		if venue != "" && Normalize(c.Venue) != venue {
			continue
		}
		if f.Weekday != WeekdayAll && !matchesWeekday(c.Date, f.Weekday) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// matchesGenre reports whether any of the artist's genre tags equals the
// requested one, case-insensitively. The facet counts computed by the
// concerts handler group by exact tag, so this has to match on exact tag
// too or the counts are wrong.
func matchesGenre(genres []string, want string) bool {
	for _, g := range genres {
		if strings.EqualFold(strings.TrimSpace(g), want) {
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
