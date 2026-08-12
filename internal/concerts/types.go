package concerts

import "time"

// Source identifies where a ticket link came from. Ordering matters — smaller
// values sort earlier (design §6).
type Source string

const (
	SourceOfficial     Source = "official" // Phase 2
	SourceTicketmaster Source = "ticketmaster"
	SourceSongkick     Source = "songkick" // Phase 2
	// SourceBandsintown is no longer produced: the public API returned an
	// AWS "explicit deny" 403 on every request and the partnership request
	// went unanswered. The constant stays because rows written before the
	// removal still carry these links, and they remain valid URLs to real
	// shows until the janitor prunes them.
	SourceBandsintown Source = "bandsintown"
)

// sourcePriority: lower is higher priority.
var sourcePriority = map[Source]int{
	SourceOfficial:     1,
	SourceTicketmaster: 2,
	SourceBandsintown:  3,
	SourceSongkick:     4,
}

// unknownSourcePriority sorts a link whose source isn't in the table *last*.
// A bare map lookup yields 0 for a miss, which is a higher priority than
// anything real — so dropping a source from the table would promote its
// legacy links above Ticketmaster rather than demoting them.
const unknownSourcePriority = 1 << 10

func priorityOf(s Source) int {
	if p, ok := sourcePriority[s]; ok {
		return p
	}
	return unknownSourcePriority
}

// TicketLink is one purchase URL surfaced to the user.
type TicketLink struct {
	Source Source `json:"source"`
	URL    string `json:"url"`
}

// ArtistRef is the artist identity carried through dedup.
type ArtistRef struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Genres []string `json:"genres,omitempty"`
}

// Act is one of the user's artists appearing at an Event. It keeps the
// artist's own DedupKey, because saves and subscriptions stay keyed per
// artist even after the artists are grouped onto one card — see Event.
type Act struct {
	Artist   ArtistRef `json:"artist"`
	DedupKey string    `json:"dedup_key"`
	// Saved and Subscribed are per-user, per-request tags applied by the
	// HTTP handler, mirroring the fields they replace on Concert.
	Saved      bool `json:"saved,omitempty"`
	Subscribed bool `json:"subscribed,omitempty"`
}

// Event is one show — a single (date, venue, city) — carrying every artist
// from the user's profile playing it. Festivals and bills where the user
// matched the headliner and the opener used to emit one Concert row per
// artist, so a single festival could fill most of a screen with what is
// really one thing the user can attend once.
//
// Grouping happens here, at assembly time, and deliberately NOT in
// DedupKey: concerts.dedup_key is the primary key of the `concerts` table
// and half of user_saved_concerts' primary key, so folding the artist out
// of it would orphan every existing save and erase the per-artist rows the
// subscribe control and genre facets are built on.
type Event struct {
	EventKey string `json:"event_key"`
	// Date is the earliest act's start time. Acts at one festival have
	// their own set times, so this is a representative instant for sorting
	// and month-grouping, not a claim about when any given act plays.
	Date      time.Time    `json:"date"`
	Venue     string       `json:"venue"`
	City      string       `json:"city"`
	State     string       `json:"state,omitempty"`
	Country   string       `json:"country,omitempty"`
	Latitude  float64      `json:"latitude,omitempty"`
	Longitude float64      `json:"longitude,omitempty"`
	Acts      []Act        `json:"acts"`
	Links     []TicketLink `json:"links"`
}

// Concert is the canonical shape returned to the frontend. One row per
// deduped (artist, date, venue, city).
type Concert struct {
	Artist    ArtistRef    `json:"artist"`
	Date      time.Time    `json:"date"`
	Venue     string       `json:"venue"`
	City      string       `json:"city"`
	State     string       `json:"state,omitempty"`
	Country   string       `json:"country,omitempty"`
	Latitude  float64      `json:"latitude,omitempty"`
	Longitude float64      `json:"longitude,omitempty"`
	Links     []TicketLink `json:"links"`
	DedupKey  string       `json:"dedup_key"`
	// Saved is a per-user, per-request tag added by the HTTP handler; it is
	// never persisted in the snapshot. omitempty keeps snapshot JSON clean.
	Saved bool `json:"saved,omitempty"`
	// Subscribed mirrors Saved but for the user's per-artist subscription
	// list (drives the "notify me instantly for this artist" feature).
	Subscribed bool `json:"subscribed,omitempty"`
}
