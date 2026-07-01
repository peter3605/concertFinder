package fallback

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
)

// TODO(config): register at https://www.songkick.com/developer and set
// SONGKICK_API_KEY in .env. Until then, SearchArtistEvents returns ErrNoAPIKey.

const songkickBase = "https://api.songkick.com/api/3.0"

// SongkickClient wraps a minimal slice of the Songkick API.
type SongkickClient struct {
	HTTP   *http.Client
	APIKey string
}

func NewSongkickClient(apiKey string) *SongkickClient {
	return &SongkickClient{HTTP: &http.Client{Timeout: 10 * time.Second}, APIKey: apiKey}
}

type songkickArtistSearchResp struct {
	ResultsPage struct {
		Results struct {
			Artist []struct {
				ID   int    `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"artist"`
		} `json:"results"`
	} `json:"resultsPage"`
}

type songkickCalendarResp struct {
	ResultsPage struct {
		Results struct {
			Event []struct {
				ID          int    `json:"id"`
				DisplayName string `json:"displayName"`
				URI         string `json:"uri"`
				Start       struct {
					Date     string `json:"date"`
					Datetime string `json:"datetime"`
				} `json:"start"`
				Venue struct {
					DisplayName string `json:"displayName"`
					MetroArea   struct {
						DisplayName string `json:"displayName"`
						State       struct {
							DisplayName string `json:"displayName"`
						} `json:"state"`
						Country struct {
							DisplayName string `json:"displayName"`
						} `json:"country"`
					} `json:"metroArea"`
				} `json:"venue"`
			} `json:"event"`
		} `json:"results"`
	} `json:"resultsPage"`
}

// SearchArtistEvents returns upcoming events for the given artist name.
// Results are pre-filtered to the US. The caller is responsible for
// haversine-filtering by radius since Songkick's calendar API doesn't take
// lat/lng directly.
func (c *SongkickClient) SearchArtistEvents(ctx context.Context, artistName string) ([]concerts.Concert, error) {
	if c.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	id, err := c.resolveArtistID(ctx, artistName)
	if err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, nil
	}
	q := url.Values{}
	q.Set("apikey", c.APIKey)
	body, err := c.get(ctx, fmt.Sprintf("%s/artists/%d/calendar.json?%s", songkickBase, id, q.Encode()))
	if err != nil {
		return nil, err
	}
	var cal songkickCalendarResp
	if err := json.Unmarshal(body, &cal); err != nil {
		return nil, err
	}
	out := make([]concerts.Concert, 0, len(cal.ResultsPage.Results.Event))
	for _, e := range cal.ResultsPage.Results.Event {
		if !strings.EqualFold(e.Venue.MetroArea.Country.DisplayName, "US") &&
			!strings.EqualFold(e.Venue.MetroArea.Country.DisplayName, "United States") {
			continue
		}
		var dt time.Time
		if e.Start.Datetime != "" {
			if t, err := time.Parse(time.RFC3339, e.Start.Datetime); err == nil {
				dt = t
			}
		}
		if dt.IsZero() && e.Start.Date != "" {
			if t, err := time.Parse("2006-01-02", e.Start.Date); err == nil {
				dt = t
			}
		}
		if dt.IsZero() {
			continue
		}
		c := concerts.Concert{
			Artist: concerts.ArtistRef{Name: artistName},
			Date:   dt,
			Venue:  e.Venue.DisplayName,
			City:   e.Venue.MetroArea.DisplayName,
			State:  e.Venue.MetroArea.State.DisplayName,
			Country: e.Venue.MetroArea.Country.DisplayName,
			Links:  []concerts.TicketLink{{Source: concerts.SourceSongkick, URL: e.URI}},
		}
		c.DedupKey = concerts.DedupKey(c.Artist.Name, c.Date, c.Venue, c.City)
		out = append(out, c)
	}
	return out, nil
}

func (c *SongkickClient) resolveArtistID(ctx context.Context, name string) (int, error) {
	q := url.Values{}
	q.Set("apikey", c.APIKey)
	q.Set("query", name)
	body, err := c.get(ctx, songkickBase+"/search/artists.json?"+q.Encode())
	if err != nil {
		return 0, err
	}
	var s songkickArtistSearchResp
	if err := json.Unmarshal(body, &s); err != nil {
		return 0, err
	}
	target := strings.ToLower(strings.TrimSpace(name))
	for _, a := range s.ResultsPage.Results.Artist {
		if strings.ToLower(strings.TrimSpace(a.DisplayName)) == target {
			return a.ID, nil
		}
	}
	return 0, nil
}

func (c *SongkickClient) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("songkick %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
