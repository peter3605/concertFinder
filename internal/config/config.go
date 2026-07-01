package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// Config holds runtime configuration sourced from process environment.
// Variables are defined in docs/design.md Appendix A plus a Phase 1 hardcoded
// location (§10.1: location picker is Phase 2).
type Config struct {
	SpotifyClientID     string
	SpotifyRedirectURI  string
	TicketmasterAPIKey  string
	BandsintownAppID    string
	DatabaseURL         string
	EncryptionKey       string
	SessionCookieDomain string
	ListenAddr          string
	UserLatitude        float64
	UserLongitude       float64
	UserRadiusMiles     int
}

// Load reads configuration from the environment.
// TODO: tighten validation once each integration lands.
func Load() (*Config, error) {
	c := &Config{
		SpotifyClientID:     os.Getenv("SPOTIFY_CLIENT_ID"),
		SpotifyRedirectURI:  os.Getenv("SPOTIFY_REDIRECT_URI"),
		TicketmasterAPIKey:  os.Getenv("TICKETMASTER_API_KEY"),
		BandsintownAppID:    os.Getenv("BANDSINTOWN_APP_ID"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		EncryptionKey:       os.Getenv("ENCRYPTION_KEY"),
		SessionCookieDomain: os.Getenv("SESSION_COOKIE_DOMAIN"),
		ListenAddr:          os.Getenv("LISTEN_ADDR"),
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	c.UserLatitude, _ = strconv.ParseFloat(os.Getenv("USER_LATITUDE"), 64)
	c.UserLongitude, _ = strconv.ParseFloat(os.Getenv("USER_LONGITUDE"), 64)
	if r, err := strconv.Atoi(os.Getenv("USER_RADIUS_MILES")); err == nil && r > 0 {
		c.UserRadiusMiles = r
	} else {
		c.UserRadiusMiles = 50
	}
	for k, v := range map[string]string{
		"SPOTIFY_CLIENT_ID":     c.SpotifyClientID,
		"SPOTIFY_REDIRECT_URI":  c.SpotifyRedirectURI,
		"TICKETMASTER_API_KEY":  c.TicketmasterAPIKey,
		"BANDSINTOWN_APP_ID":    c.BandsintownAppID,
		"ENCRYPTION_KEY":        c.EncryptionKey,
		"SESSION_COOKIE_DOMAIN": c.SessionCookieDomain,
	} {
		if v == "" {
			slog.Warn("config missing (ok during scaffolding)", "var", k)
		}
	}
	return c, nil
}
