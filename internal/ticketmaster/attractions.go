package ticketmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type attractionsResp struct {
	Embedded struct {
		Attractions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"attractions"`
	} `json:"_embedded"`
}

// ResolveAttraction looks up an attractionId for an artist name.
// Returns ("", nil) if no case-insensitive-exact-match attraction is found —
// callers should treat that as a negative resolution and cache it (design §5.2).
func (c *Client) ResolveAttraction(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	q := url.Values{}
	q.Set("keyword", name)
	q.Set("classificationName", "Music")
	q.Set("size", "10")
	q.Set("apikey", c.APIKey)
	u := APIBase + "/attractions.json?" + q.Encode()

	body, _, err := c.doGETRetry(ctx, u)
	if err != nil {
		return "", fmt.Errorf("resolve attraction %q: %w", name, err)
	}
	var out attractionsResp
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode attractions: %w", err)
	}
	target := strings.ToLower(strings.TrimSpace(name))
	for _, a := range out.Embedded.Attractions {
		if strings.ToLower(strings.TrimSpace(a.Name)) == target {
			return a.ID, nil
		}
	}
	return "", nil
}
