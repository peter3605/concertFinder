package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

	// UTC hours at which the four daily jobs run. Wall-clock times rather
	// than intervals: river's periodic scheduler re-anchors to process start,
	// so an interval schedule drifts on every deploy and never fires at all
	// on a process that restarts more often than the interval.
	//
	// DailyDigestHourUTC must trail DailyScanHourUTC by at least
	// MinScanDigestGapHours — the digest reads the snapshot the scan writes,
	// so running them together means every email describes the *previous*
	// day's scan.
	DailyAffinityHourUTC int
	DailyScanHourUTC     int
	DailyDigestHourUTC   int
	DailyJanitorHourUTC  int

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
	// Daily job times. Defaults put the fanout scan at 07:00 UTC (early
	// morning across the US) and the digest two hours later, which clears the
	// scan's 60-minute spread plus its budget and retries.
	c.DailyAffinityHourUTC = hourEnv("DAILY_AFFINITY_HOUR_UTC", 6)
	c.DailyScanHourUTC = hourEnv("DAILY_SCAN_HOUR_UTC", 7)
	c.DailyDigestHourUTC = hourEnv("DAILY_DIGEST_HOUR_UTC", 9)
	c.DailyJanitorHourUTC = hourEnv("DAILY_JANITOR_HOUR_UTC", 10)
	// Defaults sized so one full scan of a 200-artist profile fits inside a
	// day's allowance for each source; see the field comments.
	c.RateCapTMPerUserDaily = intEnv("RATE_CAP_TM_PER_USER_DAILY", 250)
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
		"ENCRYPTION_KEY":        c.EncryptionKey,
		"SESSION_COOKIE_DOMAIN": c.SessionCookieDomain,
	} {
		if v == "" {
			slog.Warn("config missing (ok during scaffolding)", "var", k)
		}
	}
	return c, nil
}

// SpotifyCallbackPath is where the OAuth callback handler is actually
// mounted (chi: /api → /auth → auth.Mount). The redirect URI registered in
// Spotify's dashboard has to end in exactly this.
const SpotifyCallbackPath = "/api/auth/callback"

// Validate reports configuration problems serious enough that the process
// should not come up. Load stays permissive — it is also used by tests and by
// half-configured local checkouts — so this is the gate main() runs before
// binding a port.
//
// It exists because the failure modes here are all silent. A missing or
// malformed ENCRYPTION_KEY used to log one warning and then skip wiring
// *every* auth and /me route: the site served, the SPA loaded, health checks
// went green, and login 404'd with nothing anywhere saying why. A redirect URI
// pointing at /callback instead of /api/auth/callback lands on the SPA's
// catch-all, so the browser finishes the OAuth dance on a logged-out page. A
// SITE_BASE_URL left at its loopback default puts "https://127.0.0.1:3000" in
// the unsubscribe link of real outbound email and in the User-Agent we send
// MusicBrainz and Nominatim. None of these announce themselves; all of them
// are trivially checkable here.
func (c Config) Validate() []error {
	var errs []error
	required := []struct{ name, val string }{
		{"SPOTIFY_CLIENT_ID", c.SpotifyClientID},
		{"SPOTIFY_REDIRECT_URI", c.SpotifyRedirectURI},
		{"TICKETMASTER_API_KEY", c.TicketmasterAPIKey},
		{"SESSION_COOKIE_DOMAIN", c.SessionCookieDomain},
	}
	for _, r := range required {
		if strings.TrimSpace(r.val) == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.name))
		}
	}

	// ENCRYPTION_KEY gates refresh-token encryption, so without a valid one
	// there is no authentication at all — never a warning.
	switch key, err := hex.DecodeString(c.EncryptionKey); {
	case c.EncryptionKey == "":
		errs = append(errs, errors.New("ENCRYPTION_KEY is required (32-byte hex; generate with: openssl rand -hex 32)"))
	case err != nil:
		errs = append(errs, fmt.Errorf("ENCRYPTION_KEY must be hex-encoded: %w", err))
	case len(key) != 32:
		errs = append(errs, fmt.Errorf("ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key)))
	}

	if u := c.SpotifyRedirectURI; u != "" && !strings.HasSuffix(strings.TrimRight(u, "/"), SpotifyCallbackPath) {
		errs = append(errs, fmt.Errorf(
			"SPOTIFY_REDIRECT_URI must end in %s (got %q) — any other path lands on the SPA catch-all and login completes into a logged-out page",
			SpotifyCallbackPath, u))
	}

	// A real cookie domain means real users; a loopback base URL then means
	// every unsubscribe link we mail points at the recipient's own machine.
	if !isLoopbackHost(c.SessionCookieDomain) && isLoopbackHost(c.SiteBaseURL) {
		errs = append(errs, fmt.Errorf(
			"SITE_BASE_URL is still the local default (%s) but SESSION_COOKIE_DOMAIN is %q — unsubscribe links and the MusicBrainz/Nominatim User-Agent would both point at localhost",
			c.SiteBaseURL, c.SessionCookieDomain))
	}

	if c.EmailDeliveryMode == "smtp" {
		if c.SMTPHost == "" {
			errs = append(errs, errors.New("SMTP_HOST is required when EMAIL_DELIVERY_MODE=smtp"))
		}
		if c.SMTPFrom == "" {
			errs = append(errs, errors.New("SMTP_FROM is required when EMAIL_DELIVERY_MODE=smtp"))
		}
	}
	return errs
}

// isLoopbackHost reports whether a domain or URL refers to the local machine.
func isLoopbackHost(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return false
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s, _, _ = strings.Cut(s, "/")
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		host = s
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

func intEnv(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v >= 0 {
		return v
	}
	return def
}

// hourEnv is intEnv clamped to a valid hour. An out-of-range value falls back
// to the default rather than being passed through: time.Date would happily
// normalize hour 25 into the next day, silently moving a job.
func hourEnv(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v >= 0 && v <= 23 {
		return v
	}
	return def
}

// MinScanDigestGapHours is how far the daily digest must trail the daily scan.
// The scan fanout smears its per-user jobs across jobs.SpreadWindow (60min),
// each job may run for jobs.ScanBudget (5min), and a failed one retries at
// attempt x 4min up to jobs.ScanMaxAttempts — roughly 90 minutes worst case.
// Two hours clears that.
//
// Under-shooting this doesn't fail loudly: the digest reads whatever snapshot
// exists, so it just describes the previous day's scan forever.
const MinScanDigestGapHours = 2

// ScanDigestGapHours returns how many hours after the daily scan the digest
// runs. Modular, so a scan at 23:00 with a digest at 01:00 reads as a 2-hour
// gap rather than a negative one.
func (c Config) ScanDigestGapHours() int {
	return ((c.DailyDigestHourUTC - c.DailyScanHourUTC) + 24) % 24
}
