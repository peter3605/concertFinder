// Package geocoding wraps OpenStreetMap's Nominatim service. Nominatim is
// free and keyless but requires a descriptive User-Agent per their usage
// policy (https://operations.osmfoundation.org/policies/nominatim/). Phase 2
// dev traffic is well within their 1 rps limit for a single-user app.
package geocoding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const NominatimEndpoint = "https://nominatim.openstreetmap.org/search"

// defaultUserAgent is the last-resort identifier. Callers should pass a real
// one built from SITE_BASE_URL + CONTACT_EMAIL (main.go does) — Nominatim's
// usage policy requires a way to reach the operator, and their response to a
// UA they can't act on is a block, which would take venue geocoding down
// with no error we control.
//
// The previous default named a repository that does not exist
// (github.com/peterho/... — the real one is peter3605), so it satisfied the
// letter of the policy and none of its purpose.
const defaultUserAgent = "ConcertFinder/1.0 (+https://github.com/peter3605/concertFinder)"

// ErrNotFound signals a successful call that yielded no match.
var ErrNotFound = errors.New("geocoding: not found")

type Result struct {
	DisplayName string
	Latitude    float64
	Longitude   float64
}

type Client struct {
	HTTP      *http.Client
	UserAgent string
}

func NewClient(userAgent string) *Client {
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &Client{HTTP: &http.Client{Timeout: 10 * time.Second}, UserAgent: userAgent}
}

type nominatimResp struct {
	DisplayName string `json:"display_name"`
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
}

// Search resolves a free-text query to a lat/lng. Returns ErrNotFound if
// Nominatim returned an empty array.
func (c *Client) Search(ctx context.Context, query string) (*Result, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("limit", "1")
	q.Set("addressdetails", "0")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, NominatimEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("nominatim: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, err
	}
	var out []nominatimResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode nominatim: %w", err)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	r := out[0]
	lat, err := strconv.ParseFloat(r.Lat, 64)
	if err != nil {
		return nil, fmt.Errorf("parse lat: %w", err)
	}
	lng, err := strconv.ParseFloat(r.Lon, 64)
	if err != nil {
		return nil, fmt.Errorf("parse lon: %w", err)
	}
	return &Result{DisplayName: r.DisplayName, Latitude: lat, Longitude: lng}, nil
}
