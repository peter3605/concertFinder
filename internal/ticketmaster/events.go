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
	Venue      Venue
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
				} `json:"start"`
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
}

// SearchEvents queries /events.json filtered by attraction, latlong, radius
// (in miles), and classificationName=Music. Returns [] if the attractionId
// is empty (caller pre-filtered).
func (c *Client) SearchEvents(ctx context.Context, attractionID string, lat, lng float64, radiusMiles int) ([]Event, error) {
	if attractionID == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("attractionId", attractionID)
	q.Set("latlong", strconv.FormatFloat(lat, 'f', 4, 64)+","+strconv.FormatFloat(lng, 'f', 4, 64))
	q.Set("radius", strconv.Itoa(radiusMiles))
	q.Set("unit", "miles")
	q.Set("classificationName", "Music")
	q.Set("size", "100")
	q.Set("countryCode", "US")
	q.Set("apikey", c.APIKey)
	u := APIBase + "/events.json?" + q.Encode()

	body, _, err := c.doGETRetry(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("tm events: %w", err)
	}
	var out eventsResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode tm events: %w", err)
	}
	events := make([]Event, 0, len(out.Embedded.Events))
	for _, e := range out.Embedded.Events {
		var start time.Time
		if e.Dates.Start.DateTime != "" {
			if t, err := time.Parse(time.RFC3339, e.Dates.Start.DateTime); err == nil {
				start = t
			}
		}
		if start.IsZero() && e.Dates.Start.LocalDate != "" {
			if t, err := time.Parse("2006-01-02", e.Dates.Start.LocalDate); err == nil {
				start = t
			}
		}
		if start.IsZero() {
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
			Venue:      v,
		})
	}
	return events, nil
}
