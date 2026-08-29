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
				EventKey:   key,
				Date:       c.Date,
				Venue:      c.Venue,
				City:       c.City,
				State:      c.State,
				Country:    c.Country,
				Latitude:   c.Latitude,
				Longitude:  c.Longitude,
				Name:       c.EventName,
				IsFestival: c.IsFestival,
				Acts:       []Act{actOf(c)},
				Links:      append([]TicketLink(nil), c.Links...),
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
		if e.Name == "" {
			e.Name = c.EventName
		}
		// Any act's source saying "festival" is enough. Ticketmaster marks
		// this on roughly 1 event in 400, so requiring agreement across a
		// bill would discard the signal almost every time it appears.
		e.IsFestival = e.IsFestival || c.IsFestival
	}
	for i := range events {
		events[i].Name = titleWorthShowing(events[i])
	}
	return events
}

// titleWorthShowing drops an event title that only repeats an act already on
// the card. Ticketmaster names an ordinary club show after its performer, so
// keeping it would put "Japanese Breakfast" directly under "Japanese
// Breakfast" on the majority of cards, while a festival or a package tour --
// the cases where the title is the whole point -- keeps it.
//
// Done here rather than in each client so the two cannot disagree about it.
func titleWorthShowing(e Event) string {
	if e.Name == "" {
		return ""
	}
	name := Normalize(e.Name)
	for _, a := range e.Acts {
		if name == Normalize(a.Artist.Name) {
			return ""
		}
	}
	return e.Name
}

func actOf(c Concert) Act {
	return Act{
		Artist:     c.Artist,
		DedupKey:   c.DedupKey,
		Saved:      c.Saved,
		Subscribed: c.Subscribed,
		Billing:    c.Billing,
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
