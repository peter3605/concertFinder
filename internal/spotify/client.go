package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const APIBase = "https://api.spotify.com/v1"

// SearchArtistResult is the minimal artist shape the /subscribe page needs.
// One image URL (medium size) if any; genres in the order Spotify returns.
type SearchArtistResult struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Genres   []string `json:"genres,omitempty"`
	ImageURL string   `json:"image_url,omitempty"`
}

// Client is a thin authenticated Spotify Web API client. Phase 1 exposes only
// what auth needs (GetMe). Affinity-source endpoints land in later files.
type Client struct {
	HTTP *http.Client
}

// NewClient panics if httpClient is nil — the default http.Client has no
// timeout and would let a hung Spotify response wedge the affinity path.
// Callers must construct one with an explicit timeout.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		panic("spotify.NewClient: httpClient must be non-nil (set an explicit timeout)")
	}
	return &Client{HTTP: httpClient}
}

type Me struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	// Email is only populated when the token includes the user-read-email
	// scope. Empty for legacy tokens issued before that scope was added.
	Email string `json:"email"`
}

// SearchArtists calls /v1/search?type=artist. Returns up to `limit` matches
// (max 20 — Spotify caps at 50 but 20 is plenty for a picker UI). Any HTTP
// error is returned verbatim so the handler can decide status codes.
func (c *Client) SearchArtists(ctx context.Context, accessToken, query string, limit int) ([]SearchArtistResult, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	q := "?type=artist&limit=" + fmt.Sprintf("%d", limit) + "&q=" + urlEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBase+"/search"+q, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify /search: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("spotify /search: %s: %s", resp.Status, string(body))
	}
	var out struct {
		Artists struct {
			Items []struct {
				ID     string   `json:"id"`
				Name   string   `json:"name"`
				Genres []string `json:"genres"`
				Images []struct {
					URL    string `json:"url"`
					Height int    `json:"height"`
				} `json:"images"`
			} `json:"items"`
		} `json:"artists"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode /search: %w", err)
	}
	results := make([]SearchArtistResult, 0, len(out.Artists.Items))
	for _, it := range out.Artists.Items {
		r := SearchArtistResult{ID: it.ID, Name: it.Name, Genres: it.Genres}
		// Pick a middle-sized image (~300px) if any; falls back to whatever
		// Spotify returned.
		if len(it.Images) > 0 {
			best := it.Images[0]
			for _, im := range it.Images {
				if im.Height > 0 && im.Height <= 320 && im.Height > best.Height {
					best = im
				}
			}
			r.ImageURL = best.URL
		}
		results = append(results, r)
	}
	return results, nil
}

// urlEscape avoids pulling url.QueryEscape's replacement of spaces with '+'
// — Spotify's search happily handles %20 too, but keep the escaping simple.
func urlEscape(s string) string {
	var b []byte
	for _, r := range s {
		switch {
		case r == ' ':
			b = append(b, '%', '2', '0')
		case r < 128 && (r == '-' || r == '_' || r == '.' || r == '~' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')):
			b = append(b, byte(r))
		default:
			for _, u := range []byte(string(r)) {
				b = append(b, '%')
				b = append(b, hexDigit(u>>4), hexDigit(u&0xF))
			}
		}
	}
	return string(b)
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'A' + n - 10
}

// ErrUserNotRegistered means Spotify accepted the login but refused the API
// call because this app is in Development Mode and the account is not on its
// User Management list. It is a deployment/configuration state, not a user
// error and not a transient failure, so retrying can never clear it — someone
// has to add the account in the Spotify developer dashboard.
var ErrUserNotRegistered = errors.New("spotify: user is not registered for this application")

// GetMe returns the current user's Spotify ID and display name.
func (c *Client) GetMe(ctx context.Context, accessToken string) (*Me, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBase+"/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify /me: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden {
		// An app in Spotify's Development Mode only admits accounts listed
		// under User Management in the developer dashboard (25 max). Everyone
		// else authorizes successfully — Spotify shows them the normal consent
		// screen and hands back a valid token — and is then refused on the
		// very first API call with it.
		//
		// Distinguished here because it is not a fault the user can act on and
		// not a bug the operator can find in a log: the whole fix is adding
		// their Spotify account email to a dashboard list. Left as a generic
		// non-2xx it reached the browser as a bare 502.
		return nil, fmt.Errorf("%w: %s", ErrUserNotRegistered, string(body))
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("spotify /me: %s: %s", resp.Status, string(body))
	}
	var m Me
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode /me: %w", err)
	}
	return &m, nil
}
