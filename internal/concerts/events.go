package concerts

// GroupEvents folds a list of per-artist concerts into per-show events,
// merging every artist playing the same (date, venue, city) onto one Event.
//
// Input order is preserved by first appearance, so a slice already sorted
// by date (which is how snapshots are stored and how Apply returns them)
// yields events in the same order — no re-sort, and no dependence on map
// iteration order.
func GroupEvents(cs []Concert) []Event {
	events := make([]Event, 0, len(cs))
	idx := make(map[string]int, len(cs))
	for _, c := range cs {
		key := EventKey(c.Date, c.Venue, c.City)
		i, ok := idx[key]
		if !ok {
			idx[key] = len(events)
			events = append(events, Event{
				EventKey:  key,
				Date:      c.Date,
				Venue:     c.Venue,
				City:      c.City,
				State:     c.State,
				Country:   c.Country,
				Latitude:  c.Latitude,
				Longitude: c.Longitude,
				Acts:      []Act{actOf(c)},
				Links:     append([]TicketLink(nil), c.Links...),
			})
			SortLinks(events[len(events)-1].Links)
			continue
		}
		e := &events[i]
		e.Acts = append(e.Acts, actOf(c))
		// Earliest set time represents the event; see Event.Date.
		if c.Date.Before(e.Date) {
			e.Date = c.Date
		}
		// Union the ticket links. Sources often give each artist on a bill
		// the same festival URL, so dedupe by URL rather than stacking one
		// identical link per act.
		for _, l := range c.Links {
			if !containsURL(e.Links, l.URL) {
				e.Links = append(e.Links, l)
			}
		}
		SortLinks(e.Links)
		// Feeds vary in how completely they describe the same room; take
		// whatever the first act didn't supply.
		if e.State == "" {
			e.State = c.State
		}
		if e.Country == "" {
			e.Country = c.Country
		}
		if e.Latitude == 0 && e.Longitude == 0 {
			e.Latitude, e.Longitude = c.Latitude, c.Longitude
		}
	}
	return events
}

func actOf(c Concert) Act {
	return Act{
		Artist:     c.Artist,
		DedupKey:   c.DedupKey,
		Saved:      c.Saved,
		Subscribed: c.Subscribed,
	}
}

// CountEventKeys returns how many distinct events the given concerts belong
// to. Facet counts use this: once the list renders one card per event, a
// pill reading "rock · 12" has to mean twelve cards, not twelve artists
// that might collapse onto three.
func CountEventKeys(cs []Concert) int {
	seen := make(map[string]struct{}, len(cs))
	for _, c := range cs {
		seen[EventKey(c.Date, c.Venue, c.City)] = struct{}{}
	}
	return len(seen)
}
