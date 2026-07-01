package fallback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TODO(config): sign up at https://api.search.brave.com and set
// BRAVE_SEARCH_API_KEY in .env. Until then, ResolveOfficialURL returns
// ErrNoAPIKey and the caller should skip Tier B URL resolution.

const braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

// ErrNoAPIKey signals the fallback caller that this tier is unavailable.
var ErrNoAPIKey = errors.New("fallback: api key not configured")

// BraveClient wraps the Brave Search API.
type BraveClient struct {
	HTTP   *http.Client
	APIKey string
}

func NewBraveClient(apiKey string) *BraveClient {
	return &BraveClient{HTTP: &http.Client{Timeout: 10 * time.Second}, APIKey: apiKey}
}

type braveResp struct {
	Web struct {
		Results []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"results"`
	} `json:"web"`
}

// ResolveOfficialURL returns the first URL that looks like the artist's own
// site — anything that isn't a known aggregator/social host. The returned URL
// may be empty if nothing plausible was found.
func (c *BraveClient) ResolveOfficialURL(ctx context.Context, artistName string) (string, error) {
	if c.APIKey == "" {
		return "", ErrNoAPIKey
	}
	q := url.Values{}
	q.Set("q", artistName+" official site")
	q.Set("count", "10")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Subscription-Token", c.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("brave: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out braveResp
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	for _, r := range out.Web.Results {
		if isLikelyOfficial(r.URL) {
			return r.URL, nil
		}
	}
	return "", nil
}

// isLikelyOfficial rejects obvious aggregator, retail, and social hosts.
func isLikelyOfficial(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	nonOfficial := []string{
		"spotify.com", "music.apple.com", "apple.com", "youtube.com", "youtu.be",
		"twitter.com", "x.com", "instagram.com", "facebook.com", "tiktok.com",
		"wikipedia.org", "genius.com", "songkick.com", "bandsintown.com",
		"ticketmaster.com", "livenation.com", "seatgeek.com", "stubhub.com",
		"amazon.com", "amazon.de", "amazon.co.uk", "reddit.com", "last.fm",
		"discogs.com", "allmusic.com", "billboard.com", "pitchfork.com",
		"dice.fm",
	}
	for _, bad := range nonOfficial {
		if host == bad || strings.HasSuffix(host, "."+bad) {
			return false
		}
	}
	return true
}
