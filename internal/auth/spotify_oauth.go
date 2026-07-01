package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const spotifyTokenURL = "https://accounts.spotify.com/api/token"

// TokenResponse mirrors Spotify's /api/token JSON. RefreshToken may be empty on
// refresh calls when Spotify does not rotate it.
type TokenResponse struct {
	AccessToken  string        `json:"access_token"`
	TokenType    string        `json:"token_type"`
	Scope        string        `json:"scope"`
	ExpiresIn    int           `json:"expires_in"`
	RefreshToken string        `json:"refresh_token"`
	ExpiresAt    time.Time     `json:"-"`
	TTL          time.Duration `json:"-"`
}

// ExchangeCode redeems an authorization code plus PKCE verifier for tokens.
func ExchangeCode(ctx context.Context, httpClient *http.Client, clientID, code, redirectURI, verifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	return postToken(ctx, httpClient, form)
}

// RefreshAccessToken uses a stored refresh token to obtain a new access token.
// Spotify may return a new refresh token; if so, callers MUST persist it.
func RefreshAccessToken(ctx context.Context, httpClient *http.Client, clientID, refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	return postToken(ctx, httpClient, form)
}

func postToken(ctx context.Context, httpClient *http.Client, form url.Values) (*TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spotifyTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("spotify token: %s: %s", resp.Status, string(body))
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	tr.TTL = time.Duration(tr.ExpiresIn) * time.Second
	tr.ExpiresAt = time.Now().Add(tr.TTL)
	return &tr, nil
}
