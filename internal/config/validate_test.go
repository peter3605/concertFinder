package config

import (
	"strings"
	"testing"
)

// prodConfig is a configuration that should pass cleanly.
func prodConfig() Config {
	return Config{
		SpotifyClientID:     "abc123",
		SpotifyRedirectURI:  "https://concerts.example.com/api/auth/callback",
		TicketmasterAPIKey:  "tmkey",
		SessionCookieDomain: "concerts.example.com",
		EncryptionKey:       strings.Repeat("ab", 32), // 32 bytes hex
		SiteBaseURL:         "https://concerts.example.com",
		EmailDeliveryMode:   "log",
	}
}

func problemsContaining(t *testing.T, c Config, want string) bool {
	t.Helper()
	for _, e := range c.Validate() {
		if strings.Contains(e.Error(), want) {
			return true
		}
	}
	return false
}

func TestValidAndCompleteConfigPasses(t *testing.T) {
	if errs := prodConfig().Validate(); len(errs) != 0 {
		t.Fatalf("a complete production config must validate, got %v", errs)
	}
}

// A missing or malformed ENCRYPTION_KEY used to log one warning and then skip
// wiring every auth and /me route: the site served, the SPA loaded, health
// checks were green, and login 404'd with nothing saying why.
func TestEncryptionKeyIsFatal(t *testing.T) {
	for _, tc := range []struct{ name, key, want string }{
		{"missing", "", "ENCRYPTION_KEY is required"},
		{"not hex", "nothexatall!!", "hex-encoded"},
		{"wrong length", strings.Repeat("ab", 16), "32 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := prodConfig()
			c.EncryptionKey = tc.key
			if !problemsContaining(t, c, tc.want) {
				t.Errorf("expected a problem mentioning %q, got %v", tc.want, c.Validate())
			}
		})
	}
}

// The callback handler is mounted at /api/auth/callback. A dashboard entry
// pointing anywhere else lands on the SPA's catch-all, so OAuth "succeeds"
// onto a logged-out page — no error, nothing in the logs.
func TestRedirectURIMustMatchTheMountedPath(t *testing.T) {
	c := prodConfig()
	c.SpotifyRedirectURI = "https://concerts.example.com/callback"
	if !problemsContaining(t, c, SpotifyCallbackPath) {
		t.Errorf("a /callback redirect URI must be rejected, got %v", c.Validate())
	}

	c.SpotifyRedirectURI = "https://127.0.0.1:3000/api/auth/callback"
	c.SessionCookieDomain = "127.0.0.1"
	c.SiteBaseURL = "https://127.0.0.1:3000"
	for _, e := range c.Validate() {
		if strings.Contains(e.Error(), SpotifyCallbackPath) {
			t.Errorf("the local dev redirect URI must be accepted, got %v", e)
		}
	}
}

// A real cookie domain means real users, so a loopback SITE_BASE_URL means
// every unsubscribe link we mail points at the recipient's own machine — and
// the User-Agent we send MusicBrainz and Nominatim advertises localhost.
func TestLoopbackSiteBaseURLRejectedInProduction(t *testing.T) {
	c := prodConfig()
	c.SiteBaseURL = "https://127.0.0.1:3000"
	if !problemsContaining(t, c, "SITE_BASE_URL") {
		t.Errorf("expected a SITE_BASE_URL problem, got %v", c.Validate())
	}
}

// ...but a fully local setup is a legitimate configuration, not a broken one.
func TestFullyLocalConfigIsAllowed(t *testing.T) {
	c := prodConfig()
	c.SessionCookieDomain = "127.0.0.1"
	c.SpotifyRedirectURI = "https://127.0.0.1:3000/api/auth/callback"
	c.SiteBaseURL = "https://127.0.0.1:3000"
	if errs := c.Validate(); len(errs) != 0 {
		t.Errorf("local dev config must validate, got %v", errs)
	}
}

func TestSMTPFieldsRequiredOnlyInSMTPMode(t *testing.T) {
	c := prodConfig()
	c.EmailDeliveryMode = "smtp"
	errs := c.Validate()
	if len(errs) != 2 {
		t.Fatalf("expected SMTP_HOST and SMTP_FROM to be required, got %v", errs)
	}
	c.SMTPHost, c.SMTPFrom = "email-smtp.us-east-1.amazonaws.com", "notify@example.com"
	if errs := c.Validate(); len(errs) != 0 {
		t.Errorf("expected clean validation once SMTP is configured, got %v", errs)
	}
	// Log mode is the default and must not demand SMTP settings.
	c = prodConfig()
	c.EmailDeliveryMode = "log"
	if errs := c.Validate(); len(errs) != 0 {
		t.Errorf("log mode must not require SMTP config, got %v", errs)
	}
}

// Every problem at once, so a misconfigured deploy takes one round trip to
// fix rather than one per variable.
func TestValidateReportsEveryProblem(t *testing.T) {
	if errs := (Config{}).Validate(); len(errs) < 5 {
		t.Errorf("an empty config should report every missing core var, got %d: %v", len(errs), errs)
	}
}
