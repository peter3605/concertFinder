package concerts

import "time"

// Source identifies where a ticket link came from. Ordering matters — smaller
// values sort earlier (design §6).
type Source string

const (
	SourceOfficial     Source = "official"    // Phase 2
	SourceTicketmaster Source = "ticketmaster"
	SourceBandsintown  Source = "bandsintown"
	SourceSongkick     Source = "songkick" // Phase 2
)

// sourcePriority: lower is higher priority.
var sourcePriority = map[Source]int{
	SourceOfficial:     1,
	SourceTicketmaster: 2,
	SourceBandsintown:  3,
	SourceSongkick:     4,
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
}
