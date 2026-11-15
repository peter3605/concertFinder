package email

import (
	"fmt"
	"sort"
	"strings"

	"github.com/peterho/concertfinder/internal/concerts"
)

// DigestMaxEvents is the most events one digest lists (CF-31). There was no
// limit while every row came from an artist the user follows; a genre pick
// in a dense city is hundreds of shows -- New York rock alone is ~600 -- and
// an email listing them all is one nobody reads and most clients clip.
const DigestMaxEvents = 25

// CapDigest keeps the concerts belonging to the first max events, in the
// order given, and reports how many further events were dropped. It cuts on
// events rather than concerts so a festival is never mailed with half its
// matched acts.
//
// The caller records only kept as sent. The rest stay unsent and lead the
// next digest, so a large genre feed is mailed a page a week instead of being
// marked delivered by an email that never listed it.
func CapDigest(cs []concerts.Concert, max int) (kept []concerts.Concert, moreEvents int) {
	seen := make(map[string]struct{}, max)
	dropped := make(map[string]struct{})
	kept = make([]concerts.Concert, 0, len(cs))
	for _, c := range cs {
		k := concerts.EventKey(c.LocalDay(), c.Venue, c.City)
		if _, ok := seen[k]; ok {
			kept = append(kept, c)
			continue
		}
		if len(seen) < max {
			seen[k] = struct{}{}
			kept = append(kept, c)
			continue
		}
		dropped[k] = struct{}{}
	}
	return kept, len(dropped)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// maxListedActs caps how many artists a multi-act entry names before it
// summarizes the remainder, mirroring VISIBLE_ACTS in the web event card. A
// large festival can match dozens of profile artists and an email is a worse
// place than a card to dump them all.
const maxListedActs = 4

// actsLine renders an event's acts as prose: "A", "A and B", "A, B and C",
// "A, B, C, D and 3 more".
func actsLine(acts []concerts.Act) string {
	names := make([]string, 0, len(acts))
	for _, a := range acts {
		names = append(names, a.Artist.Name)
	}
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		return names[0]
	case len(names) > maxListedActs:
		return strings.Join(names[:maxListedActs], ", ") +
			fmt.Sprintf(" and %d more", len(names)-maxListedActs)
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// digestSection is one titled run of month groups. A digest that is not
// split has one section with no title.
type digestSection struct {
	title  string
	months []monthGroup
}

const (
	chosenSectionTitle = "Your artists"
	genreSectionTitle  = "In genres you follow"
)

func digestSections(cs []concerts.Concert, events []concerts.Event, chosen func(concerts.Concert) bool) []digestSection {
	if chosen == nil {
		return []digestSection{{months: groupByMonth(events)}}
	}
	lead := make(map[string]bool)
	for _, c := range cs {
		if chosen(c) {
			lead[concerts.EventKey(c.LocalDay(), c.Venue, c.City)] = true
		}
	}
	var mine, genre []concerts.Event
	for _, e := range events {
		if lead[concerts.EventKey(e.LocalDay(), e.Venue, e.City)] {
			mine = append(mine, e)
		} else {
			genre = append(genre, e)
		}
	}
	if len(mine) == 0 || len(genre) == 0 {
		return []digestSection{{months: groupByMonth(events)}}
	}
	return []digestSection{
		{title: chosenSectionTitle, months: groupByMonth(mine)},
		{title: genreSectionTitle, months: groupByMonth(genre)},
	}
}

type monthGroup struct {
	label string
	items []concerts.Event
}

func groupByMonth(es []concerts.Event) []monthGroup {
	buckets := map[string][]concerts.Event{}
	labels := map[string]string{}
	for _, e := range es {
		key := e.LocalDayTime().Format("2006-01")
		buckets[key] = append(buckets[key], e)
		labels[key] = e.LocalDayTime().Format("January 2006")
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]monthGroup, 0, len(keys))
	for _, k := range keys {
		g := buckets[k]
		sort.Slice(g, func(i, j int) bool { return g[i].Date.Before(g[j].Date) })
		out = append(out, monthGroup{label: labels[k], items: g})
	}
	return out
}
