package concerts

import (
	"encoding/json"
	"time"

	"github.com/peterho/concertfinder/internal/geocoding"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// CachePrefixTicketmaster is the cache_key prefix loadOrFetchTM writes under.
// It is here rather than inlined at the call site because the discover view
// reads those rows back by prefix, and a prefix that only agrees with
// cacheKey by coincidence would return an empty list forever with nothing to
// see in a log.
const CachePrefixTicketmaster = "tm:"

// DiscoverMaxActs caps how many acts one cached event contributes. A festival
// bill can run to dozens of attractions; the signed-out card is a teaser, and
// the ones after the fourth are neither useful there nor free to carry.
const DiscoverMaxActs = 4

// FromCachedTicketmaster turns raw cached Ticketmaster payloads into a
// deduped, date-sorted candidate set, dropping anything starting before
// notBefore and anything whose venue has no coordinates.
//
// Deliberately location-independent: the caller decodes once and serves many
// different coordinates out of the result with Near. Filtering here instead
// would tie a process-wide cache to whichever visitor happened to warm it.
//
// This is the whole of the signed-out discover view's data path: it reads
// blobs some *other* user's scan already paid for and filters them by
// geography. No affinity, no upstream call, no quota — an unauthenticated
// endpoint that could reach Ticketmaster would be a quota drain with a URL.
//
// The acts on a card are Ticketmaster's own lineup for the show, not
// anybody's listening: ArtistRef.ID is deliberately left empty, because the
// IDs everywhere else in this package are Spotify's and a Ticketmaster
// attraction ID wearing the same field would be a save or subscribe pointed
// at an artist that does not exist.
//
// A payload that fails to decode is skipped. These rows are written by a
// previous release of this binary, so a shape change is a real possibility
// and losing one artist's cached events is not worth failing the request.
func FromCachedTicketmaster(blobs [][]byte, notBefore time.Time) []Concert {
	m := NewMerger()
	for _, blob := range blobs {
		var evs []ticketmaster.Event
		if err := json.Unmarshal(blob, &evs); err != nil {
			continue
		}
		for _, e := range evs {
			if e.Start.Before(notBefore) {
				continue
			}
			// No coordinates means no way to tell whether this is the
			// visitor's city or the other side of the country. The cache
			// key's own location cannot stand in: it is where the *scanning*
			// user was, and one scan's radius covers a lot of ground.
			if e.Venue.Latitude == 0 && e.Venue.Longitude == 0 {
				continue
			}
			for _, c := range discoverConcerts(e) {
				m.Add(c)
			}
		}
	}
	return m.All()
}

// Near returns the concerts within loc's radius of loc, preserving order.
// Used by the discover view to serve one decoded candidate set to visitors
// in different places.
func Near(cs []Concert, loc Location) []Concert {
	out := make([]Concert, 0, len(cs))
	for _, c := range cs {
		if c.Latitude == 0 && c.Longitude == 0 {
			continue
		}
		if geocoding.HaversineMiles(loc.Latitude, loc.Longitude, c.Latitude, c.Longitude) <= float64(loc.RadiusMiles) {
			out = append(out, c)
		}
	}
	return out
}

// discoverConcerts expands one cached event into one Concert per act on the
// bill. An event with no lineup at all becomes a single Concert named after
// the event, which is what Ticketmaster titles an ordinary club show.
func discoverConcerts(e ticketmaster.Event) []Concert {
	names := make([]string, 0, len(e.Lineup))
	for _, at := range e.Lineup {
		if at.Name != "" {
			names = append(names, at.Name)
		}
	}
	billed := names
	if len(billed) == 0 {
		if e.Name == "" {
			return nil
		}
		billed = []string{e.Name}
	}
	if len(billed) > DiscoverMaxActs {
		billed = billed[:DiscoverMaxActs]
	}
	out := make([]Concert, 0, len(billed))
	for _, name := range billed {
		c := Concert{
			Artist:    ArtistRef{Name: name},
			Date:      e.Start,
			Venue:     e.Venue.Name,
			City:      e.Venue.City,
			State:     e.Venue.State,
			Country:   e.Venue.Country,
			Latitude:  e.Venue.Latitude,
			Longitude: e.Venue.Longitude,
			Links:     []TicketLink{{Source: SourceTicketmaster, URL: e.URL}},

			EventName:  e.Name,
			IsFestival: e.IsFestival,
		}
		c.Billing = billingOf(names, name)
		c.DedupKey = DedupKey(c.Artist.Name, c.Date, c.Venue, c.City)
		out = append(out, c)
	}
	return out
}
