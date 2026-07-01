package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const APIBase = "https://api.spotify.com/v1"

// Client is a thin authenticated Spotify Web API client. Phase 1 exposes only
// what auth needs (GetMe). Affinity-source endpoints land in later files.
type Client struct {
	HTTP *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{HTTP: httpClient}
}

type Me struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

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
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("spotify /me: %s: %s", resp.Status, string(body))
	}
	var m Me
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode /me: %w", err)
	}
	return &m, nil
}
