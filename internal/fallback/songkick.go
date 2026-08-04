package fallback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
)

// TODO(config): register at https://www.songkick.com/developer and set
// SONGKICK_API_KEY in .env. Until then, SearchArtistEvents returns ErrNoAPIKey.

const songkickBase = "https://api.songkick.com/api/3.0"

const (
	songkickMaxRetries    = 3
	songkickMaxRetryAfter = 30 * time.Second
	songkickBaseBackoff   = 200 * time.Millisecond
	songkickUserAgent     = "ConcertFinder/0.1 (https://github.com/peter3605/concertFinder)"
)

// SongkickClient wraps a minimal slice of the Songkick API. Includes retry
// with backoff on 5xx / 429 and a UA header identifying us so Songkick can
// contact us before rate-limiting.
type SongkickClient struct {
	HTTP   *http.Client
	APIKey string
}

// NewSongkickClient panics on a nil httpClient — a hung Songkick call would
// otherwise burn a scan worker's whole budget.
func NewSongkickClient(apiKey string) *SongkickClient {
	return &SongkickClient{HTTP: &http.Client{Timeout: 10 * time.Second}, APIKey: apiKey}
}

// Enabled reports whether this client can actually reach Songkick. Callers
// check it before spending per-user quota: without a key every request
// returns ErrNoAPIKey, and charging for those burns the daily allowance on
// calls that never leave the process — which then reads downstream as a
// genuine rate cap.
func (c *SongkickClient) Enabled() bool { return c != nil && c.APIKey != "" }

type songkickArtistSearchResp struct {
	ResultsPage struct {
		Results struct {
			Artist []struct {
				ID          int    `json:"id"`
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
		cn := e.Venue.MetroArea.Country.DisplayName
		if cn != "US" && cn != "USA" && cn != "United States" {
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
		concert := concerts.Concert{
			Artist:  concerts.ArtistRef{Name: artistName},
			Date:    dt,
			Venue:   e.Venue.DisplayName,
			City:    e.Venue.MetroArea.DisplayName,
			State:   e.Venue.MetroArea.State.DisplayName,
			Country: e.Venue.MetroArea.Country.DisplayName,
			Links:   []concerts.TicketLink{{Source: concerts.SourceSongkick, URL: e.URI}},
		}
		concert.DedupKey = concerts.DedupKey(concert.Artist.Name, concert.Date, concert.Venue, concert.City)
		out = append(out, concert)
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
	// Normalize both sides so "Sigur Rós" matches "Sigur Ros" and "The XX"
	// matches "XX" — same rules as the dedup key so semantics stay uniform.
	target := concerts.Normalize(name)
	for _, a := range s.ResultsPage.Results.Artist {
		if concerts.Normalize(a.DisplayName) == target {
			return a.ID, nil
		}
	}
	return 0, nil
}

// get is retry-aware: honors Retry-After on 429, exponential backoff for
// 5xx / network errors.
func (c *SongkickClient) get(ctx context.Context, u string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= songkickMaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", songkickUserAgent)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if !songkickBackoff(ctx, attempt) {
				return nil, lastErr
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if !songkickBackoff(ctx, attempt) {
				return nil, lastErr
			}
			continue
		}
		switch {
		case resp.StatusCode/100 == 2:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("songkick 429")
			d := time.Duration(0)
			if raw := resp.Header.Get("Retry-After"); raw != "" {
				if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
					d = time.Duration(secs) * time.Second
				}
			}
			// Honor Retry-After, clamped to songkickMaxRetryAfter — clamping
			// shortens the wait toward 30s, it does not discard it. See
			// spotify/http.go for why the previous form was wrong.
			if d > 0 {
				if d > songkickMaxRetryAfter {
					d = songkickMaxRetryAfter
				}
				t := time.NewTimer(d)
				select {
				case <-t.C:
				case <-ctx.Done():
					t.Stop()
					return nil, ctx.Err()
				}
				t.Stop()
				continue
			}
			if !songkickBackoff(ctx, attempt) {
				return nil, fmt.Errorf("songkick 429: retries exhausted")
			}
			continue
		case resp.StatusCode/100 == 5:
			lastErr = fmt.Errorf("songkick %d", resp.StatusCode)
			if !songkickBackoff(ctx, attempt) {
				return nil, lastErr
			}
			continue
		default:
			return nil, fmt.Errorf("songkick %d: %s", resp.StatusCode, u)
		}
	}
	if lastErr == nil {
		lastErr = errors.New("songkick: retries exhausted")
	}
	return nil, lastErr
}

func songkickBackoff(ctx context.Context, attempt int) bool {
	if attempt >= songkickMaxRetries {
		return false
	}
	d := songkickBaseBackoff << attempt
	d += time.Duration(rand.Int63n(int64(100 * time.Millisecond)))
	if d > songkickMaxRetryAfter {
		d = songkickMaxRetryAfter
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
