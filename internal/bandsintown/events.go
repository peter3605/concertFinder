package bandsintown

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Event carries only what the concerts package needs. URL retains BIT's
// tracking parameters verbatim per §9.3.
type Event struct {
	ID       string
	URL      string
	Datetime time.Time
	Venue    Venue
}

type Venue struct {
	Name      string
	City      string
	Region    string
	Country   string
	Latitude  float64
	Longitude float64
}

type wireEvent struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Datetime string `json:"datetime"`
	Venue    struct {
		Name      string `json:"name"`
		City      string `json:"city"`
		Region    string `json:"region"`
		Country   string `json:"country"`
		Latitude  string `json:"latitude"`
		Longitude string `json:"longitude"`
	} `json:"venue"`
}

// ArtistEvents returns upcoming events for the given artist. Results are
// filtered to the US and within radiusMiles of (lat,lng). BIT's API has no
// geo filter, so filtering happens client-side.
func (c *Client) ArtistEvents(ctx context.Context, artistName string, lat, lng float64, radiusMiles int) ([]Event, error) {
	if artistName == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("app_id", c.AppID)
	q.Set("date", "upcoming")
	u := fmt.Sprintf("%s/artists/%s/events?%s", APIBase, url.PathEscape(artistName), q.Encode())

	body, _, err := c.doGETRetry(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("bit events %q: %w", artistName, err)
	}
	var wire []wireEvent
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("decode bit events: %w", err)
	}
	out := make([]Event, 0, len(wire))
	for _, w := range wire {
		if !strings.EqualFold(w.Venue.Country, "United States") && !strings.EqualFold(w.Venue.Country, "US") {
			continue
		}
		v := Venue{Name: w.Venue.Name, City: w.Venue.City, Region: w.Venue.Region, Country: w.Venue.Country}
		if f, err := strconv.ParseFloat(w.Venue.Latitude, 64); err == nil {
			v.Latitude = f
		}
		if f, err := strconv.ParseFloat(w.Venue.Longitude, 64); err == nil {
			v.Longitude = f
		}
		if v.Latitude != 0 && v.Longitude != 0 && radiusMiles > 0 {
			if HaversineMiles(lat, lng, v.Latitude, v.Longitude) > float64(radiusMiles) {
				continue
			}
		}
		// BIT datetimes are ISO-like without TZ; parse permissively.
		dt, err := time.Parse("2006-01-02T15:04:05", w.Datetime)
		if err != nil {
			dt, err = time.Parse(time.RFC3339, w.Datetime)
			if err != nil {
				continue
			}
		}
		out = append(out, Event{ID: w.ID, URL: w.URL, Datetime: dt, Venue: v})
	}
	return out, nil
}

// HaversineMiles returns great-circle distance between two lat/lng points.
func HaversineMiles(lat1, lng1, lat2, lng2 float64) float64 {
	const earthMiles = 3958.8
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthMiles * c
}
