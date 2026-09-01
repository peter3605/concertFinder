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
		SiteDomain:          "concerts.example.com",
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

// SITE_DOMAIN is Caddy's, not the binary's, but nothing else can check it:
// scripts/check-deploy-config.sh validates the Caddyfile against a synthetic
// .env it writes itself, so it proves the wiring and never sees the real file.
// Empty, `{$SITE_DOMAIN} {` collapses into a global options block and Caddy
// dies with "unrecognized global option: encode" — a crash loop next to a
// perfectly healthy api container.
func TestSiteDomainRequiredInProduction(t *testing.T) {
	c := prodConfig()
	c.SiteDomain = ""
	if !problemsContaining(t, c, "SITE_DOMAIN is required") {
		t.Errorf("a production config without SITE_DOMAIN must be rejected, got %v", c.Validate())
	}
}

// A cert for one name and emailed links pointing at another is silent in both
// directions: Caddy serves happily, and the mail goes out looking fine.
func TestSiteDomainMustAgreeWithSiteBaseURL(t *testing.T) {
	c := prodConfig()
	c.SiteDomain = "concerts.example.com"
	c.SiteBaseURL = "https://www.example.org"
	if !problemsContaining(t, c, "disagree") {
		t.Errorf("mismatched SITE_DOMAIN and SITE_BASE_URL must be rejected, got %v", c.Validate())
	}

	// Case and a trailing port are not disagreements.
	c.SiteBaseURL = "https://Concerts.Example.com"
	for _, e := range c.Validate() {
		if strings.Contains(e.Error(), "disagree") {
			t.Errorf("host comparison must be case-insensitive, got %v", e)
		}
	}
}

// Local dev runs no Caddy at all, so it must not be asked for SITE_DOMAIN.
func TestSiteDomainNotRequiredLocally(t *testing.T) {
	c := prodConfig()
	c.SessionCookieDomain = "127.0.0.1"
	c.SpotifyRedirectURI = "https://127.0.0.1:3000/api/auth/callback"
	c.SiteBaseURL = "https://127.0.0.1:3000"
	c.SiteDomain = ""
	if errs := c.Validate(); len(errs) != 0 {
		t.Errorf("local dev config must validate without SITE_DOMAIN, got %v", errs)
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

// A partial APNs set is the dangerous state: push.New refuses it, the worker
// wires up with a nil client, and every notification is dropped silently
// while every other job runs normally. Nothing distinguishes that from
// "nobody has opted in yet", so it has to be caught at startup.
func TestAPNsIsAllOrNothing(t *testing.T) {
	c := prodConfig()
	c.APNSKeyID = "ABC123"
	c.APNSTeamID = "TEAM123"
	c.APNSBundleID = "com.example.app"
	c.APNSP8Key = "" // the missing one

	if !problemsContaining(t, c, "APNs is partially configured") {
		t.Fatalf("expected a partial-APNs error, got %v", c.Validate())
	}
	// The message must name the variable nobody set; "APNs is misconfigured"
	// sends the reader back to four possibilities.
	if !problemsContaining(t, c, "APNS_P8_KEY") {
		t.Fatalf("error should name the missing variable, got %v", c.Validate())
	}
}

func TestAPNsFullyUnsetIsFine(t *testing.T) {
	if problemsContaining(t, prodConfig(), "APNs") {
		t.Fatalf("a deployment with no APNs config should validate, got %v", prodConfig().Validate())
	}
}

func TestAPNsFullySetIsFine(t *testing.T) {
	c := prodConfig()
	c.APNSKeyID = "ABC123"
	c.APNSTeamID = "TEAM123"
	c.APNSBundleID = "com.example.app"
	c.APNSP8Key = "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----"
	if problemsContaining(t, c, "APNs") {
		t.Fatalf("a fully configured APNs set should validate, got %v", c.Validate())
	}
}

// APNS_ENVIRONMENT names which environments the deployment's APNs key is
// authorized for, and both is the setting a "Sandbox & Production" key wants
// -- it is what lets one server reach TestFlight builds and a developer's
// debug build at the same time. A typo must not be accepted: it would leave
// half the fleet unreachable, and nothing downstream reports that.
func TestAPNSEnvironmentAcceptsOneOrBoth(t *testing.T) {
	for _, env := range []string{"", "sandbox", "production", "sandbox,production"} {
		c := prodConfig()
		c.APNSEnvironment = env
		if problemsContaining(t, c, "APNS_ENVIRONMENT") {
			t.Errorf("APNS_ENVIRONMENT=%q rejected: %v", env, c.Validate())
		}
	}
	for _, env := range []string{"development", "both", "prod"} {
		c := prodConfig()
		c.APNSEnvironment = env
		if !problemsContaining(t, c, "APNS_ENVIRONMENT") {
			t.Errorf("APNS_ENVIRONMENT=%q accepted", env)
		}
	}
}

// iOS fetches apple-app-site-association from the universal link's own host,
// so a callback URL on a different domain ends every mobile login in Safari
// with the app still waiting — and nothing logs it.
func TestMobileCallbackURLMustMatchSiteHost(t *testing.T) {
	c := prodConfig()
	c.IOSAppID = "TEAM123.com.example.app"
	c.MobileCallbackURL = "https://elsewhere.example/app/auth/callback"

	if !problemsContaining(t, c, "differs from SITE_BASE_URL host") {
		t.Fatalf("expected a host-mismatch error, got %v", c.Validate())
	}
}

func TestMobileCallbackURLMustBeHTTPS(t *testing.T) {
	c := prodConfig()
	c.IOSAppID = "TEAM123.com.example.app"
	c.MobileCallbackURL = "http://concerts.example.com/app/auth/callback"

	if !problemsContaining(t, c, "must be https") {
		t.Fatalf("expected an https error, got %v", c.Validate())
	}
}

// A callback with no app ID means the association file 404s, so the link
// never reaches the app at all.
func TestMobileCallbackURLRequiresAppID(t *testing.T) {
	c := prodConfig()
	c.MobileCallbackURL = c.SiteBaseURL + "/app/auth/callback"
	c.IOSAppID = ""

	if !problemsContaining(t, c, "IOS_APP_ID is empty") {
		t.Fatalf("expected a missing-app-id error, got %v", c.Validate())
	}
}

// The matching pair, so the tests above are known to fail for the reason
// stated rather than because any iOS config trips the validator.
func TestFullyConfiguredMobileFlowPasses(t *testing.T) {
	c := prodConfig()
	c.IOSAppID = "TEAM123.com.example.app"
	c.MobileCallbackURL = c.SiteBaseURL + "/app/auth/callback"

	if errs := c.Validate(); len(errs) != 0 {
		t.Fatalf("a complete mobile config must validate, got %v", errs)
	}
}
