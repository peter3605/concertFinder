package concerts

import "strings"

// PickedReason is the reason line for an artist the user picked by hand
// (docs/public-path.md §5).
const PickedReason = "You picked them"

// ViewerMatches is what one viewer's own choices say about the acts in their
// snapshot: who they subscribed to, who they picked, and the affinity reason
// line for their Spotify profile artists. It is the single place that turns
// those three lookups into per-act facts, so the feed's reason line, the
// digest's ordering and anything after them (CF-B27's reason line in the
// email) cannot disagree about why an artist is there.
//
// Every map is keyed by the viewer-facing artist ID that ApplyArtistRefs lays
// on a concert, and every map may be nil: a failed lookup costs a reason
// line or an ordering hint, never the listing.
type ViewerMatches struct {
	Subscribed map[string]struct{}
	// Picked holds TicketmasterArtistID values for the user's artist picks.
	Picked map[string]struct{}
	// Reasons is affinity.Reasons over the user's profile. Absent for an
	// artist whose only contribution has no wording.
	Reasons map[string]string
}

// Tag sets Subscribed and Reason on each concert. Reason prefers the
// affinity line, then PickedReason, and is otherwise left as it was.
func (v ViewerMatches) Tag(cs []Concert) {
	for i := range cs {
		id := cs[i].Artist.ID
		if _, ok := v.Subscribed[id]; ok {
			cs[i].Subscribed = true
		}
		if reason, ok := v.Reasons[id]; ok {
			cs[i].Reason = reason
		} else if _, ok := v.Picked[id]; ok {
			cs[i].Reason = PickedReason
		}
	}
}

// Chosen reports whether the user chose this concert's artist, rather than
// being shown it because it played in a genre they follow: subscribed,
// picked, or in their Spotify profile.
//
// Profile membership is read off the ID rather than the cached profile. A
// snapshot's artist refs come from exactly three kinds of scan target: picks
// and genre listings, both of which carry TicketmasterArtistID ("tm:") IDs,
// and the Spotify profile, whose IDs are Spotify's. So a non-empty ID without
// that prefix is a profile artist by construction, and this holds even when
// the 24h affinity cache has expired, which a weekly digest reading Reasons
// alone could not promise.
func (v ViewerMatches) Chosen(c Concert) bool {
	id := c.Artist.ID
	if id == "" {
		return false
	}
	if _, ok := v.Subscribed[id]; ok {
		return true
	}
	if _, ok := v.Picked[id]; ok {
		return true
	}
	return IsProfileArtistID(id)
}

// IsProfileArtistID reports whether a viewer-facing artist ID came from the
// Spotify profile rather than from Ticketmaster (a pick or a genre listing).
func IsProfileArtistID(id string) bool {
	return id != "" && !strings.HasPrefix(id, TicketmasterArtistID(""))
}

// ChosenFirst reorders cs so every event with at least one chosen act comes
// before every event without one, keeping the given order within each group.
// It moves whole events, keyed as GroupEvents keys them, so a festival is
// never split between the two halves. Callers pass a date-ordered list (a
// snapshot is), which makes each half soonest-first.
func ChosenFirst(cs []Concert, chosen func(Concert) bool) []Concert {
	keyOf := func(c Concert) string { return EventKey(c.LocalDay(), c.Venue, c.City) }
	chosenEvent := make(map[string]bool, len(cs))
	for _, c := range cs {
		if chosen(c) {
			chosenEvent[keyOf(c)] = true
		}
	}
	out := make([]Concert, 0, len(cs))
	for _, c := range cs {
		if chosenEvent[keyOf(c)] {
			out = append(out, c)
		}
	}
	for _, c := range cs {
		if !chosenEvent[keyOf(c)] {
			out = append(out, c)
		}
	}
	return out
}
