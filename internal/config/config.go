package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
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

	// Phase 2 fallback chain (design §5.4). Off by default.
	Phase2Enabled     bool
	Phase2MinScore    float64
	BraveSearchAPIKey string
	SongkickAPIKey    string
	// Phase2FallbackBudgetSeconds caps total fallback wall-clock per scan.
	// The chain's lookups are globally serialized at 1 req/sec, so without
	// a cap they can consume an entire ScanBudget on a cold profile and
	// starve the TM/BIT fan-out that produces most results. 0 = package
	// default (60s); negative disables the fallback outright.
	Phase2FallbackBudgetSeconds int
	// Phase2FallbackConcurrency is how many scans may use the fallback
	// chain at once, process-wide. The budget above is per-scan wall-clock
	// but the resolvers behind it share one 1 req/sec turnstile each, so
	// letting every scan in divides that throughput without reducing anyone's
	// budget — coverage silently thins as users are added. 1 matches the
	// turnstiles; <= 0 removes the limit.
	Phase2FallbackConcurrency int

	// SWR staleness threshold for user_concert_snapshots. When a request
	// finds a snapshot older than this, it enqueues a background refresh
	// (and still returns the stale snapshot immediately).
	SnapshotStaleAfterHours int

	// Per-user daily caps on outbound API calls (design §8.3). 0 disables
	// enforcement for that source.
	//
	// These must be sized against spotify.MaxScoredArtists (200), not picked
	// for the shared-account ceiling alone. A scan needs roughly one call per
	// artist per source once the concert cache lapses; the previous TM cap of
	// 100 was below that, so a single user could never cover their own
	// profile in a day and every scan reported itself incomplete. 250 covers
	// 200 artists plus resolution calls and leaves headroom.
	//
	// The trade-off is real: TM's account-wide budget is 5000/day, so 250
	// per user supports ~20 concurrently active users rather than ~50. With
	// DefaultCacheTTL at 12h a user costs far less than their cap on a
	// typical day, but the ceiling is worth revisiting before onboarding a
	// crowd — the ledger enforces per-user limits, not the account total.
	RateCapTMPerUserDaily       int
	RateCapBITPerUserDaily      int
	RateCapSongkickPerUserDaily int

	// ConcertCacheTTLHours is how long cached per-artist upstream responses
	// stay trusted. 0 = package default (12h). Longer means fewer quota-
	// spending scans; shorter means fresher listings.
	ConcertCacheTTLHours int

	// Email digest (design §10.3, Phase 3). SMTP config; when empty the
	// sender runs in "log" mode and writes messages to slog instead.
	EmailDeliveryMode string // "smtp" or "log" (default: log)
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string
	SMTPFrom          string
	// SiteBaseURL is the public base (e.g. https://your-domain.com) prepended
	// to unsubscribe links in outgoing digests. Local dev can leave blank
	// (falls back to https://127.0.0.1:3000).
	SiteBaseURL string

	// ContactEmail is rendered on Privacy/Terms pages and used as the SES
	// operator contact. Defaults to peter.ho433@gmail.com for local dev.
	ContactEmail string

	// SigningKey is used for HMAC over CSRF and unsubscribe tokens.
	// Separated from EncryptionKey so a signing-key rotation doesn't
	// invalidate stored refresh-token ciphertexts. 32 bytes hex; falls back
	// to EncryptionKey with a KDF suffix when unset so single-key deploys
	// still work.
	SigningKey string
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

	c.Phase2Enabled = os.Getenv("PHASE2_FALLBACKS_ENABLED") == "1" || strings.EqualFold(os.Getenv("PHASE2_FALLBACKS_ENABLED"), "true")
	if f, err := strconv.ParseFloat(os.Getenv("PHASE2_MIN_SCORE"), 64); err == nil {
		c.Phase2MinScore = f
	} else {
		c.Phase2MinScore = 2.0
	}
	c.BraveSearchAPIKey = os.Getenv("BRAVE_SEARCH_API_KEY")
	c.SongkickAPIKey = os.Getenv("SONGKICK_API_KEY")
	// Not intEnv: that clamps negatives to the default, and negative is a
	// meaningful value here ("skip the fallback entirely").
	if v, err := strconv.Atoi(os.Getenv("PHASE2_FALLBACK_BUDGET_SECONDS")); err == nil {
		c.Phase2FallbackBudgetSeconds = v
	}
	// Same reasoning: 0 and negative both mean "no admission limit", which
	// intEnv would swallow.
	c.Phase2FallbackConcurrency = 1
	if v, err := strconv.Atoi(os.Getenv("PHASE2_FALLBACK_CONCURRENCY")); err == nil {
		c.Phase2FallbackConcurrency = v
	}
	if h, err := strconv.Atoi(os.Getenv("SNAPSHOT_STALE_AFTER_HOURS")); err == nil && h > 0 {
		c.SnapshotStaleAfterHours = h
	} else {
		c.SnapshotStaleAfterHours = 6
	}
	// Defaults sized so one full scan of a 200-artist profile fits inside a
	// day's allowance for each source; see the field comments.
	c.RateCapTMPerUserDaily = intEnv("RATE_CAP_TM_PER_USER_DAILY", 250)
	c.RateCapBITPerUserDaily = intEnv("RATE_CAP_BIT_PER_USER_DAILY", 250)
	c.RateCapSongkickPerUserDaily = intEnv("RATE_CAP_SONGKICK_PER_USER_DAILY", 100)
	c.ConcertCacheTTLHours = intEnv("CONCERT_CACHE_TTL_HOURS", 0)

	c.EmailDeliveryMode = strings.ToLower(strings.TrimSpace(os.Getenv("EMAIL_DELIVERY_MODE")))
	if c.EmailDeliveryMode == "" {
		c.EmailDeliveryMode = "log"
	}
	c.SMTPHost = os.Getenv("SMTP_HOST")
	c.SMTPPort = intEnv("SMTP_PORT", 587)
	c.SMTPUsername = os.Getenv("SMTP_USERNAME")
	c.SMTPPassword = os.Getenv("SMTP_PASSWORD")
	c.SMTPFrom = os.Getenv("SMTP_FROM")
	c.SiteBaseURL = strings.TrimRight(os.Getenv("SITE_BASE_URL"), "/")
	if c.SiteBaseURL == "" {
		c.SiteBaseURL = "https://127.0.0.1:3000"
	}
	c.ContactEmail = os.Getenv("CONTACT_EMAIL")
	if c.ContactEmail == "" {
		c.ContactEmail = "peter.ho433@gmail.com"
	}
	c.SigningKey = os.Getenv("SIGNING_KEY")
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

func intEnv(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v >= 0 {
		return v
	}
	return def
}
