package ticketmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// MusicSegmentID is Ticketmaster's classification ID for the Music segment.
// Its genres are what a user may pick.
const MusicSegmentID = "KZFzniwnSyZfZ7v7nJ"

// MaxGenrePages bounds one genre listing (docs/public-path.md §1.2). New York
// jazz is 1,590 events and still 1,028 inside a 60-day window, so no date
// window bounds it; three pages of the date-ascending listing is the next
// 300 shows, which is as much as a feed or a digest can use.
const MaxGenrePages = 3

// Genre is one top-level Music genre.
type Genre struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type segmentResp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Embedded struct {
		Genres []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
	} `json:"_embedded"`
}

// MusicGenres returns the Music segment's top-level genres, sorted by name.
// Sub-genres are not offered: measured in New York, Indie Rock had 2 events
// and American Punk, Bebop and Acid House had none, so a sub-genre pick is a
// feed that stays empty. "Undefined" is a real genre in the list and is
// dropped for the same reason it is dropped from facets.
func (c *Client) MusicGenres(ctx context.Context) ([]Genre, error) {
	q := url.Values{}
	q.Set("apikey", c.APIKey)
	u := APIBase + "/classifications/segments/" + MusicSegmentID + ".json?" + q.Encode()
	body, _, err := c.doGETRetry(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("tm genres: %w", err)
	}
	var resp segmentResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode tm genres: %w", err)
	}
	out := make([]Genre, 0, len(resp.Embedded.Genres))
	for _, g := range resp.Embedded.Genres {
		name := strings.TrimSpace(g.Name)
		if g.ID == "" || name == "" || strings.EqualFold(name, "Undefined") {
			continue
		}
		out = append(out, Genre{ID: g.ID, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SearchGenreEvents lists music events of one genre near a coordinate, date
// ascending, stopping after MaxGenrePages.
//
// This is the one Ticketmaster listing that is truncated on purpose, and the
// rule that a truncated fetch is never cached has a written exception for it
// (docs/public-path.md §1.2). That rule protects against arbitrary
// fragments: a failed page or a refused permit leaves a listing missing an
// unknown middle. A date-sorted prefix cut at its page cap is not a fragment
// -- it is correct up to its last date -- so it is reported as cacheable,
// with a horizon saying where correctness ends.
//
// horizon is the venue-local day of the last row when the page cap cut the
// listing short, and "" when the listing is whole. Rows on the horizon day
// itself may be incomplete, so a reader keeps only rows strictly before it.
// The query is one metro area, so one timezone: date order by instant is
// local-date order, and the last row's day is the first day not covered.
//
// cacheable is false when the stop was anything other than the listing's end
// or the page cap -- the same condition SearchEvents reports as incomplete.
// The events are still returned, and horizon still describes them, so a scan
// can use them; they must not be cached.
func (c *Client) SearchGenreEvents(ctx context.Context, genreID string, lat, lng float64, radiusMiles int, permit PagePermit) (events []Event, horizon string, cacheable bool, err error) {
	if genreID == "" {
		return nil, "", true, nil
	}
	q := c.eventsQuery(lat, lng, radiusMiles)
	q.Set("genreId", genreID)
	return c.datePrefix(ctx, q, permit, MaxGenrePages)
}

// datePrefix fetches a query date ascending up to maxPages and describes the
// result as a prefix: horizon is the venue-local day of the last row when the
// cap (or a short stop) cut the listing, "" when it is whole, and cacheable is
// false only for a short stop -- a refused permit or a failed later page,
// which leave a fragment rather than a prefix.
//
// Shared by SearchGenreEvents and SearchEventsNear, the two listings that are
// truncated on purpose, so the rule that makes a capped listing cacheable is
// stated once.
func (c *Client) datePrefix(ctx context.Context, q url.Values, permit PagePermit, maxPages int) (events []Event, horizon string, cacheable bool, err error) {
	q.Set("sort", "date,asc")
	evs, stop, err := c.pagedEventsN(ctx, q, permit, maxPages)
	if stop != stopDone && len(evs) > 0 {
		horizon = evs[len(evs)-1].LocalDate
	}
	return evs, horizon, stop != stopShort, err
}

// seatPackage matches attractions that are a seat or venue upgrade rather
// than an act. Denver's country listing returned "Club Level Seating: Ball
// Arena" as an attraction (docs/public-path.md §1.2); in a genre listing,
// where acts come from the lineup rather than from a user's pick, that would
// be an act on the user's feed with a bell next to it.
//
// Whole words only, so a band whose name merely contains one of these
// strings ("Parkway Drive") is not caught.
var seatPackage = regexp.MustCompile(`(?i)\b(club level|seating|suites?|parking|premium seats?|hospitality package|vip package|lounge access)\b`)

// IsSeatPackage reports whether an attraction or event name is a seat
// package rather than a performer.
func IsSeatPackage(name string) bool {
	return seatPackage.MatchString(name)
}
