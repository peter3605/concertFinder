package ticketmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Event is Ticketmaster's own shape — kept in this package per the "no shared
// models" rule. Callers translate to the canonical concerts.Concert.
type Event struct {
	ID   string
	Name string
	URL  string
	// Lineup is every attraction on the bill, in the order Ticketmaster
	// returned them. That order is NOT documented as billing order and is not
	// always it -- see concerts.billingOf, which is where the caveats live.
	// Kept raw here; this package reports what the API said.
	Lineup []Attraction
	// IsFestival is Ticketmaster's own classification subType, not a guess
	// from the name. It is sparse -- roughly 1 event in 400 carries it -- so
	// false means "not marked", never "definitely not a festival".
	IsFestival bool
	Start      time.Time
	// LocalDate is the calendar day at the venue, "2006-01-02", taken
	// verbatim from dates.start.localDate. It is what identifies the show,
	// and Start is not: Ticketmaster sends dates.start.dateTime in UTC, so a
	// 20:00 Pacific show on the 15th is 03:00Z on the 16th and formatting
	// Start names the wrong day for most evening shows in the US.
	//
	// It is a string rather than a derived time.Time on purpose. It is half
	// of concerts.DedupKey, so it has to survive every round trip through
	// JSONB and TIMESTAMPTZ unchanged -- an offset carried on a time.Time
	// survives JSON but not a timestamptz column, and a single .UTC() added
	// anywhere downstream would silently reinstate this bug. A string cannot
	// drift.
	LocalDate string
	Venue     Venue
}

// Attraction is one act on an event's bill.
type Attraction struct {
	ID   string
	Name string
}

type Venue struct {
	Name      string
	City      string
	State     string
	Country   string
	Latitude  float64
	Longitude float64
}

type eventsResp struct {
	Embedded struct {
		Events []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			URL   string `json:"url"`
			Dates struct {
				Start struct {
					DateTime  string `json:"dateTime"`
					LocalDate string `json:"localDate"`
					// LocalTime is absent on a timeTBA event and appears
					// later, once the set time is announced. Reading it is
					// what lets Start improve without LocalDate -- and so
					// the dedup key -- moving underneath a save.
					LocalTime string `json:"localTime"`
				} `json:"start"`
				// Timezone is the IANA name for the venue, e.g.
				// "America/Los_Angeles". Only used to place Start on the
				// clock; never to compute LocalDate.
				Timezone string `json:"timezone"`
			} `json:"dates"`
			Classifications []struct {
				SubType struct {
					Name string `json:"name"`
				} `json:"subType"`
			} `json:"classifications"`
			Embedded struct {
				Attractions []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"attractions"`
				Venues []struct {
					Name string `json:"name"`
					City struct {
						Name string `json:"name"`
					} `json:"city"`
					State struct {
						StateCode string `json:"stateCode"`
						Name      string `json:"name"`
					} `json:"state"`
					Country struct {
						CountryCode string `json:"countryCode"`
						Name        string `json:"name"`
					} `json:"country"`
					Location struct {
						Latitude  string `json:"latitude"`
						Longitude string `json:"longitude"`
					} `json:"location"`
				} `json:"venues"`
			} `json:"_embedded"`
		} `json:"events"`
	} `json:"_embedded"`
	// Page is Ticketmaster's own paging metadata. Decoding it is what makes
	// truncation observable: without it the client cannot tell a complete
	// 40-event answer from the first 100 of 250.
	Page struct {
		Size          int `json:"size"`
		TotalElements int `json:"totalElements"`
		TotalPages    int `json:"totalPages"`
		Number        int `json:"number"`
	} `json:"page"`
}

// MaxEventPages bounds how many events.json pages one SearchEvents will
// fetch. Ten is not an arbitrary safety valve: the Discovery API refuses deep
// paging past 1000 results, and at size=100 that is exactly pages 0-9. A loop
// that trusted totalPages would spend a permit on page 10 to be told no.
const MaxEventPages = 10

// eventsPageSize is the API maximum. Kept as a constant because MaxEventPages
// is derived from it -- the two only mean what they say together.
const eventsPageSize = 100

// PagePermit is consulted before each events.json request after the first.
// Returning false stops pagination and hands back the pages already
// collected.
//
// It exists because quota is charged per upstream request, and this package
// deliberately knows nothing about the rate ledger -- the same split as
// fallback.SongkickClient, which makes two requests while fallback.Chain is
// what charges for them. The production caller passes a closure over
// rate.Allow, so a refused page is recorded as a denial and surfaces as an
// incomplete scan through the machinery that already exists.
//
// A nil PagePermit fetches the first page only. That is the safe direction to
// default: an unpermitted caller under-reads one artist's listing, where the
// alternative silently overspends an allowance shared by every user of the
// deployment. Nothing is hidden either way -- complete is false whenever
// pagination stopped early.
type PagePermit func() bool

// SearchEvents queries /events.json filtered by attraction, latlong, radius
// (in miles), and classificationName=Music. Returns [] if the attractionId
// is empty (caller pre-filtered).
//
// complete reports whether the full result set was retrieved. It is false
// when pagination stopped early -- the permit refused, MaxEventPages was
// reached, or a later page failed -- and a caller that caches this result
// must not cache it when false, or it stores the truncation for the life of
// the cache entry.
//
// Before this followed pagination it set size=100 and read the first page
// only, so an attraction with more than 100 dated events in radius (a
// residency, a festival act) had the remainder silently dropped: no error, no
// log, just a short listing.
func (c *Client) SearchEvents(ctx context.Context, attractionID string, lat, lng float64, radiusMiles int, permit PagePermit) (events []Event, complete bool, err error) {
	if attractionID == "" {
		return nil, true, nil
	}
	q := c.eventsQuery(lat, lng, radiusMiles)
	q.Set("attractionId", attractionID)
	return c.pagedEvents(ctx, q, permit)
}

// SearchEventsNear queries /events.json for music events near a coordinate
// with no attraction filter -- everything on sale in a city rather than one
// artist's dates.
//
// This is the signed-out discover view's supply. That view reads
// concert_cache and nothing else, so it can only show what somebody's scan
// already paid for, and a scan is filtered to one user's artists in one
// user's city: until real accounts scan real places, the landing page is
// empty everywhere. Seeding those places is what this exists for, and it is
// deliberately the only query in this package that is not about a specific
// artist.
//
// Same paging, permit and `complete` contract as SearchEvents. A caller that
// caches the result must not cache it when complete is false, for the reason
// loadOrFetchTM records: a truncated listing written to the cache is served
// for the life of the entry and no later fetch can correct it.
func (c *Client) SearchEventsNear(ctx context.Context, lat, lng float64, radiusMiles int, permit PagePermit) (events []Event, complete bool, err error) {
	return c.pagedEvents(ctx, c.eventsQuery(lat, lng, radiusMiles), permit)
}

// eventsQuery builds the parameters every /events.json request shares. Kept
// in one place so the city-wide query cannot drift from the per-artist one --
// a differing classificationName or countryCode would quietly seed the
// discover view with a different population than a scan produces.
func (c *Client) eventsQuery(lat, lng float64, radiusMiles int) url.Values {
	q := url.Values{}
	q.Set("latlong", strconv.FormatFloat(lat, 'f', 4, 64)+","+strconv.FormatFloat(lng, 'f', 4, 64))
	q.Set("radius", strconv.Itoa(radiusMiles))
	q.Set("unit", "miles")
	q.Set("classificationName", "Music")
	q.Set("size", strconv.Itoa(eventsPageSize))
	q.Set("countryCode", "US")
	q.Set("apikey", c.APIKey)
	return q
}

// pagedEvents runs the /events.json paging loop for an already-composed query.
//
// Shared by both callers on purpose. Every property the comments above defend
// -- one permit per request after the first, page 0 failing hard while a later
// page degrades to incomplete, totalPages==0 reading as "nothing further",
// MaxEventPages matching the API's 1000-result deep-paging refusal -- is
// stated once here rather than in two copies that agree today.
func (c *Client) pagedEvents(ctx context.Context, q url.Values, permit PagePermit) (events []Event, complete bool, err error) {
	var out []Event
	for page := 0; page < MaxEventPages; page++ {
		if page > 0 && (permit == nil || !permit()) {
			return out, false, nil
		}
		// page=0 is the default; omitting it keeps the first request
		// byte-for-byte what it has always been.
		if page > 0 {
			q.Set("page", strconv.Itoa(page))
		}
		u := APIBase + "/events.json?" + q.Encode()

		body, _, err := c.doGETRetry(ctx, u)
		if err != nil {
			if page == 0 {
				return nil, false, fmt.Errorf("tm events: %w", err)
			}
			// Later pages already cost permits and page 0 is real data.
			// Report it incomplete rather than discarding the lot.
			return out, false, fmt.Errorf("tm events page %d: %w", page, err)
		}
		var resp eventsResp
		if err := json.Unmarshal(body, &resp); err != nil {
			if page == 0 {
				return nil, false, fmt.Errorf("decode tm events: %w", err)
			}
			return out, false, fmt.Errorf("decode tm events page %d: %w", page, err)
		}
		out = append(out, decodeEvents(resp)...)

		// totalPages is 0 on an empty result set, which the loop must read as
		// "nothing further", not as "keep going".
		if resp.Page.TotalPages <= page+1 {
			return out, true, nil
		}
	}
	// Fell out at MaxEventPages with pages still outstanding.
	return out, false, nil
}

// decodeEvents turns one decoded page into Events, skipping the rows that
// cannot be used. Split out of SearchEvents so every page goes through the
// identical mapping -- a second copy for subsequent pages is how a field
// stops being populated past the first 100 results.
func decodeEvents(resp eventsResp) []Event {
	events := make([]Event, 0, len(resp.Embedded.Events))
	for _, e := range resp.Embedded.Events {
		start, localDate := startAndLocalDate(
			e.Dates.Start.DateTime,
			e.Dates.Start.LocalDate,
			e.Dates.Start.LocalTime,
			e.Dates.Timezone,
		)
		if start.IsZero() || localDate == "" {
			continue // skip events without a usable date
		}
		var v Venue
		if len(e.Embedded.Venues) > 0 {
			ven := e.Embedded.Venues[0]
			v.Name = ven.Name
			v.City = ven.City.Name
			v.State = ven.State.StateCode
			if v.State == "" {
				v.State = ven.State.Name
			}
			v.Country = ven.Country.CountryCode
			if v.Country == "" {
				v.Country = ven.Country.Name
			}
			if lat, err := strconv.ParseFloat(ven.Location.Latitude, 64); err == nil {
				v.Latitude = lat
			}
			if lng, err := strconv.ParseFloat(ven.Location.Longitude, 64); err == nil {
				v.Longitude = lng
			}
		}
		lineup := make([]Attraction, 0, len(e.Embedded.Attractions))
		for _, a := range e.Embedded.Attractions {
			if a.Name == "" {
				continue
			}
			lineup = append(lineup, Attraction{ID: a.ID, Name: a.Name})
		}
		isFestival := false
		for _, cl := range e.Classifications {
			if strings.EqualFold(cl.SubType.Name, "Festival") {
				isFestival = true
				break
			}
		}
		events = append(events, Event{
			Lineup:     lineup,
			IsFestival: isFestival,
			ID:         e.ID,
			Name:       e.Name,
			URL:        e.URL,
			Start:      start,
			LocalDate:  localDate,
			Venue:      v,
		})
	}
	return events
}

// startAndLocalDate resolves one event's instant and its venue-local calendar
// day from the four fields Ticketmaster may send.
//
// The two are deliberately computed from different inputs, because they
// answer different questions and only one of them is allowed to move:
//
//   - localDate identifies the show and is returned verbatim. It is stable
//     across a timeTBA event gaining a set time, which is the whole point:
//     dedup_key is the primary key of `concerts` and half of
//     user_saved_concerts', so a key that moves orphans the user's save and
//     re-notifies them about a show they were already told about.
//   - start is a representative instant for sorting and display, and may
//     legitimately sharpen from midnight to 20:00 when TM announces the time.
//
// Preference order for start is localDate+localTime in the venue's zone
// (right instant, right wall clock), then dateTime (right instant, UTC wall
// clock), then localDate alone at midnight UTC.
//
// A timezone that will not load is not an error and must never fall through
// to affecting localDate: the runtime image is distroless, so a missing
// zoneinfo would degrade every event at once, and the failure would show up
// only in production. Losing the wall clock costs display precision; letting
// it reach the key would cost saves.
func startAndLocalDate(dateTime, localDate, localTime, timezone string) (time.Time, string) {
	var start time.Time
	if localDate != "" && localTime != "" && timezone != "" {
		if loc, err := time.LoadLocation(timezone); err == nil {
			// TM sends localTime as either HH:MM:SS or HH:MM.
			for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
				if t, err := time.ParseInLocation(layout, localDate+" "+localTime, loc); err == nil {
					start = t
					break
				}
			}
		}
	}
	if start.IsZero() && dateTime != "" {
		if t, err := time.Parse(time.RFC3339, dateTime); err == nil {
			start = t
		}
	}
	if start.IsZero() && localDate != "" {
		if t, err := time.Parse("2006-01-02", localDate); err == nil {
			start = t
		}
	}
	if localDate == "" && !start.IsZero() {
		// No localDate from TM at all. Fall back to the instant's own wall
		// clock, which is right when start carries a real offset and is the
		// best available guess when it does not.
		localDate = start.Format("2006-01-02")
	}
	return start, localDate
}
