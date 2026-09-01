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

	"github.com/peterho/concertfinder/internal/push"
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
	// Account-wide daily ceilings. These are the numbers the upstream
	// actually enforces -- Ticketmaster's 5000/day is per API key, not per
	// user of ours -- so per-user caps alone let N users multiply past it.
	RateCapTMAccountDaily       int
	RateCapSongkickAccountDaily int

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

	// SiteDomain is the bare apex this deployment serves. Nothing in this
	// binary uses it — it belongs to Caddy, whose site block is literally
	// `{$SITE_DOMAIN} {`. It is read here anyway because it arrives in the
	// same .env, and both of its failure modes are silent from inside the
	// app: empty, the site block degenerates into a global options block and
	// Caddy crash-loops behind a healthy-looking api container; disagreeing
	// with SiteBaseURL, the certificate is issued for a name that none of the
	// emailed links point at. Validate checks both.
	SiteDomain string

	// ContactEmail is rendered on Privacy/Terms pages and used as the SES
	// operator contact. Defaults to peter.ho433@gmail.com for local dev.
	ContactEmail string

	// --- iOS client (docs/ios-app-plan.md Appendix A) ---

	// APNs credentials. Token-based auth: one key per team, no annual
	// certificate rotation. APNSP8Key is the PEM contents, not a path — it
	// arrives as an SSM SecureString like every other secret here. Never log
	// it.
	APNSKeyID       string
	APNSTeamID      string
	APNSBundleID    string
	APNSP8Key       string
	APNSEnvironment string // "sandbox" | "production"

	// IOSAppID is "<TeamID>.<BundleID>", served in the
	// apple-app-site-association file.
	IOSAppID string

	// MobileCallbackURL is the universal link /api/auth/callback redirects to
	// after an app-initiated login, e.g. https://<domain>/app/auth/callback.
	// Empty disables the mobile auth flow outright rather than letting it
	// complete into a session the app cannot read.
	MobileCallbackURL string

	// MinIOSBuild is the oldest client build this server supports, returned
	// by /api/site-info. 0 means no floor.
	MinIOSBuild int

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
	// Defaults are the documented upstream allowances, so an operator who
	// sets nothing is bounded by the real limit rather than by nothing.
	c.RateCapTMAccountDaily = intEnv("RATE_CAP_TM_ACCOUNT_DAILY", 5000)
	c.RateCapSongkickAccountDaily = intEnv("RATE_CAP_SONGKICK_ACCOUNT_DAILY", 5000)
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
	c.SiteDomain = strings.TrimSpace(os.Getenv("SITE_DOMAIN"))
	c.ContactEmail = os.Getenv("CONTACT_EMAIL")
	if c.ContactEmail == "" {
		c.ContactEmail = "peter.ho433@gmail.com"
	}
	c.APNSKeyID = strings.TrimSpace(os.Getenv("APNS_KEY_ID"))
	c.APNSTeamID = strings.TrimSpace(os.Getenv("APNS_TEAM_ID"))
	c.APNSBundleID = strings.TrimSpace(os.Getenv("APNS_BUNDLE_ID"))
	c.APNSP8Key = os.Getenv("APNS_P8_KEY")
	c.APNSEnvironment = strings.ToLower(strings.TrimSpace(os.Getenv("APNS_ENVIRONMENT")))
	if c.APNSEnvironment == "" {
		// Production is the safe default: a sandbox client pointed at the
		// production host fails loudly with BadDeviceToken, whereas a
		// production build silently pushing to the sandbox host reaches
		// nobody and reports success.
		c.APNSEnvironment = "production"
	}
	c.IOSAppID = strings.TrimSpace(os.Getenv("IOS_APP_ID"))
	if c.IOSAppID == "" && c.APNSTeamID != "" && c.APNSBundleID != "" {
		// The two halves are already configured for APNs; deriving it saves
		// a variable whose only failure mode is disagreeing with them.
		c.IOSAppID = c.APNSTeamID + "." + c.APNSBundleID
	}
	c.MobileCallbackURL = strings.TrimRight(strings.TrimSpace(os.Getenv("MOBILE_CALLBACK_URL")), "/")
	c.MinIOSBuild = intEnv("MIN_IOS_BUILD", 0)

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

	// SITE_DOMAIN is Caddy's variable, not this binary's, and it is the one
	// piece of the deployment config that nothing else can check. The CI
	// preflight (scripts/check-deploy-config.sh) validates the Caddyfile
	// against a *synthetic* .env it writes itself, so it proves the wiring and
	// can never see whether the deployment's own .env actually sets this. The
	// api container reads the same file, so it can — and an error here is a
	// legible message in `docker compose logs api` instead of Caddy's
	// "unrecognized global option: encode" from a variable that isn't
	// mentioned anywhere in the message.
	//
	// Gated on a real cookie domain: local dev runs no Caddy at all.
	if !isLoopbackHost(c.SessionCookieDomain) {
		switch base := hostOf(c.SiteBaseURL); {
		case c.SiteDomain == "":
			errs = append(errs, errors.New(
				"SITE_DOMAIN is required in production — the Caddyfile's site block is {$SITE_DOMAIN}, and an empty one collapses into a global options block that refuses to start"))
		case base != "" && !strings.EqualFold(base, c.SiteDomain):
			errs = append(errs, fmt.Errorf(
				"SITE_DOMAIN (%q) and the host in SITE_BASE_URL (%q) disagree — Caddy would serve a certificate for one name while every emailed link and the MusicBrainz/Nominatim User-Agent point at the other",
				c.SiteDomain, base))
		}
	}

	if c.EmailDeliveryMode == "smtp" {
		if c.SMTPHost == "" {
			errs = append(errs, errors.New("SMTP_HOST is required when EMAIL_DELIVERY_MODE=smtp"))
		}
		if c.SMTPFrom == "" {
			errs = append(errs, errors.New("SMTP_FROM is required when EMAIL_DELIVERY_MODE=smtp"))
		}
	}

	// APNs is all-or-nothing. A partial set is the dangerous state: push.New
	// refuses it, the push worker wires up with a nil client and no-ops, and
	// every notification is silently dropped while every other job runs
	// normally. Nothing in the logs distinguishes that from "no user has
	// opted in yet".
	apns := []struct{ name, val string }{
		{"APNS_KEY_ID", c.APNSKeyID},
		{"APNS_TEAM_ID", c.APNSTeamID},
		{"APNS_BUNDLE_ID", c.APNSBundleID},
		{"APNS_P8_KEY", c.APNSP8Key},
	}
	set, missing := 0, []string{}
	for _, v := range apns {
		if strings.TrimSpace(v.val) != "" {
			set++
		} else {
			missing = append(missing, v.name)
		}
	}
	if set > 0 && len(missing) > 0 {
		errs = append(errs, fmt.Errorf(
			"APNs is partially configured: %s missing. Push would wire up and then silently drop every notification — set all four or none",
			strings.Join(missing, ", ")))
	}
	// Empty is not checked: Load always defaults it, so an empty value here
	// means a hand-built Config (tests, half-configured checkouts) rather
	// than a deployment that set it wrong. Parsing is push's, so the accepted
	// spellings cannot drift from what the client will actually do with them.
	if c.APNSEnvironment != "" {
		if _, err := push.ParseEnvironments(c.APNSEnvironment); err != nil {
			errs = append(errs, fmt.Errorf(
				"APNS_ENVIRONMENT names which environments the APNs key is authorized for: %w", err))
		}
	}

	// The universal link must live on the domain the AASA file is served
	// from, or iOS will not hand the redirect to the app: it fetches the
	// association from the link's own host. A mismatch ends the login in
	// Safari on a page that bounces to the feed, with the app still waiting.
	if c.MobileCallbackURL != "" {
		switch base := hostOf(c.SiteBaseURL); {
		case !strings.HasPrefix(c.MobileCallbackURL, "https://"):
			errs = append(errs, fmt.Errorf(
				"MOBILE_CALLBACK_URL must be https (got %q) — iOS only claims universal links over TLS",
				c.MobileCallbackURL))
		case base != "" && !strings.EqualFold(hostOf(c.MobileCallbackURL), base):
			errs = append(errs, fmt.Errorf(
				"MOBILE_CALLBACK_URL host (%q) differs from SITE_BASE_URL host (%q) — iOS fetches apple-app-site-association from the link's own domain, so the redirect would open Safari instead of the app",
				hostOf(c.MobileCallbackURL), base))
		}
		if c.IOSAppID == "" {
			errs = append(errs, errors.New(
				"MOBILE_CALLBACK_URL is set but IOS_APP_ID is empty — apple-app-site-association would 404 and the universal link would never reach the app"))
		}
	}
	return errs
}

// hostOf extracts the hostname from either a bare domain or a full URL,
// dropping scheme, path and port. Returns "" for the empty string.
func hostOf(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s, _, _ = strings.Cut(s, "/")
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	return s
}

// isLoopbackHost reports whether a domain or URL refers to the local machine.
func isLoopbackHost(s string) bool {
	switch hostOf(s) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
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
