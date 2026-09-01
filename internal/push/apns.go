// Package push delivers Apple Push Notification service messages.
//
// Token-based authentication (an ES256 JWT), not certificate-based: one key
// covers every app and both environments, and it does not expire annually the
// way a push certificate does.
//
// No third-party APNs library. Go's stdlib speaks HTTP/2 natively and the
// protocol surface actually used here is one POST with four headers, so a
// dependency would buy nothing and cost a supply chain — the same reasoning
// that keeps the AWS SDK out of /internal.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// APNs hosts. A token is minted by the device against one environment and is
// meaningless to the other — sending a sandbox token to the production host
// returns BadDeviceToken, it does not fall back.
const (
	HostProduction = "https://api.push.apple.com"
	HostSandbox    = "https://api.sandbox.push.apple.com"
)

// The two environment names, in the vocabulary the app registers with and
// user_devices stores. Apple spells the entitlement "development"; the
// mapping to "sandbox" happens on the client, in PushRegistrar.
const (
	EnvSandbox    = "sandbox"
	EnvProduction = "production"
)

// hostFor maps a device's environment to the host that will accept its token.
//
// It returns an error rather than defaulting, and that is the whole point.
// The obvious spelling — production unless told otherwise — turns a caller
// who forgets to set Notification.Environment into a sandbox token sent to
// the production host, which answers BadDeviceToken, which
// Error.IsUnregistered reports as a dead token, which retires the device
// permanently. A silent default here costs a user their notifications for
// good.
func hostFor(env string) (string, error) {
	switch env {
	case EnvSandbox:
		return HostSandbox, nil
	case EnvProduction:
		return HostProduction, nil
	}
	return "", fmt.Errorf("push: unknown APNs environment %q", env)
}

// jwtLifetime is how long a generated authentication token is reused.
//
// Apple rejects tokens older than 1 hour (ExpiredProviderToken) and also
// rejects a provider that mints them too often (TooManyProviderTokenUpdates),
// so this is bounded on both sides. Fifty minutes leaves room for clock skew
// against the first limit without approaching the second.
const jwtLifetime = 50 * time.Minute

// Client sends notifications to APNs.
//
// One client serves both environments. The host is chosen per notification
// from the environment the device registered with, not once from
// configuration — see Send. Everything an APNs request is authenticated with
// (the key, its ID, the team, the bundle, and the JWT minted from them) is
// identical for sandbox and production, so there is nothing per-environment
// to hold.
type Client struct {
	// HTTPClient must speak HTTP/2. The zero value of http.Client does over
	// TLS, which is the only transport APNs offers.
	HTTPClient *http.Client
	KeyID      string
	TeamID     string
	BundleID   string

	// environments the signing key is authorized for. Not "where we send" —
	// routing is per device — but which hosts this .p8 will be accepted by,
	// which is a property of how the key was issued in Apple's portal and is
	// the only thing an operator has to state.
	//
	// The zero value serves nothing, which is the safe direction: a
	// hand-built Client sends no pushes rather than sending them somewhere
	// that would retire the tokens.
	environments map[string]bool

	signingKey *ecdsa.PrivateKey

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// Serves reports whether this deployment's key is authorized for an
// environment. The push worker filters devices on it, so a key restricted to
// one environment skips the other's devices — visibly, and without writing
// anything — instead of sending to a host that answers InvalidProviderToken.
func (c *Client) Serves(env string) bool { return c.environments[env] }

// Environments lists what Serves accepts, sorted, for logging.
func (c *Client) Environments() []string {
	out := make([]string, 0, len(c.environments))
	for env := range c.environments {
		out = append(out, env)
	}
	sort.Strings(out)
	return out
}

// Config is the deployment's APNs settings, sourced from the environment.
type Config struct {
	KeyID    string
	TeamID   string
	BundleID string
	P8Key    string // PEM contents of the .p8, not a path
	// Environment is APNS_ENVIRONMENT: which environments the key is
	// authorized for. "sandbox", "production", or both as a comma-separated
	// list. See ParseEnvironments.
	Environment string
}

// ParseEnvironments reads APNS_ENVIRONMENT.
//
// It is a *list* because an APNs auth key issued as "Sandbox & Production"
// can serve both, and a deployment with such a key should serve both: the
// alternative is the flip this replaced, where moving to production for
// TestFlight silently stopped push for every debug build, and moving the
// entitlement without the server variable (or the reverse) broke push
// entirely with nothing but BadDeviceToken to show for it. A key issued for
// one environment only still says so, and gets the old behaviour.
//
// Empty means production, matching config.Load's default so the two cannot
// disagree about what an unset variable means.
func ParseEnvironments(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{EnvProduction}, nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		env := strings.ToLower(strings.TrimSpace(part))
		if _, err := hostFor(env); err != nil {
			return nil, fmt.Errorf(
				"push: %q is not an APNs environment; use %q, %q, or %q",
				part, EnvSandbox, EnvProduction, EnvSandbox+","+EnvProduction)
		}
		if seen[env] {
			continue
		}
		seen[env] = true
		out = append(out, env)
	}
	return out, nil
}

// New builds a Client from configuration. Returns an error rather than a
// half-configured client: a push worker that starts and silently drops every
// notification is worse than one that refuses to wire up.
func New(cfg Config) (*Client, error) {
	if cfg.KeyID == "" || cfg.TeamID == "" || cfg.BundleID == "" || cfg.P8Key == "" {
		return nil, errors.New("push: APNS_KEY_ID, APNS_TEAM_ID, APNS_BUNDLE_ID and APNS_P8_KEY are all required")
	}
	key, err := parseP8(cfg.P8Key)
	if err != nil {
		return nil, err
	}
	envs, err := ParseEnvironments(cfg.Environment)
	if err != nil {
		return nil, err
	}
	served := make(map[string]bool, len(envs))
	for _, e := range envs {
		served[e] = true
	}
	return &Client{
		// Timeout covers the whole request. APNs is fast; a stalled
		// connection here would otherwise hold a river worker slot.
		HTTPClient:   &http.Client{Timeout: 30 * time.Second},
		KeyID:        cfg.KeyID,
		TeamID:       cfg.TeamID,
		BundleID:     cfg.BundleID,
		environments: served,
		signingKey:   key,
	}, nil
}

// parseP8 decodes the PKCS#8 PEM Apple issues for a push key.
func parseP8(pemData string) (*ecdsa.PrivateKey, error) {
	// Tolerate a key pasted with escaped newlines, which is what happens
	// when a PEM goes through an env var or a Parameter Store value.
	pemData = strings.ReplaceAll(pemData, `\n`, "\n")
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("push: APNS_P8_KEY is not valid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("push: parse p8: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("push: expected an ECDSA key, got %T", parsed)
	}
	return key, nil
}

// authToken returns a cached JWT, minting a new one when the current one is
// near expiry. Apple rate-limits token *generation* separately from sends, so
// this must not sign per request.
func (c *Client) authToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	now := time.Now()
	header := map[string]string{"alg": "ES256", "kid": c.KeyID, "typ": "JWT"}
	claims := map[string]any{"iss": c.TeamID, "iat": now.Unix()}

	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)

	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, c.signingKey, sum[:])
	if err != nil {
		return "", fmt.Errorf("push: sign jwt: %w", err)
	}
	// JWS ES256 is the fixed-width concatenation of r and s, each
	// left-padded to the curve's byte size — *not* the ASN.1 encoding
	// ecdsa.SignASN1 produces. Apple rejects the latter as InvalidProviderToken.
	keyBytes := (c.signingKey.Curve.Params().BitSize + 7) / 8
	sig := make([]byte, 2*keyBytes)
	copyPadded(sig[:keyBytes], r)
	copyPadded(sig[keyBytes:], s)

	c.token = signingInput + "." + enc.EncodeToString(sig)
	c.tokenExp = now.Add(jwtLifetime)
	return c.token, nil
}

func copyPadded(dst []byte, n *big.Int) {
	b := n.Bytes()
	copy(dst[len(dst)-len(b):], b)
}

// Notification is one push to one device.
type Notification struct {
	DeviceToken string
	// Environment is the device's own, from user_devices.environment, and it
	// selects the host. It travels with the token because it is a property of
	// the token: the two are minted together and neither means anything at
	// the other host. Sending the same Notification to several devices means
	// setting both fields each time, not just the token.
	Environment string
	// CollapseID coalesces notifications on the device: a later push with the
	// same ID replaces an undelivered earlier one rather than stacking.
	CollapseID string
	Payload    Payload
}

// Payload is the APNs message body.
//
// Kept thin on purpose. APNs caps the payload at 4KB and a festival card is
// not small, so the custom keys carry an event_key the app resolves against
// the feed it already has rather than the event itself.
type Payload struct {
	APS       APS    `json:"aps"`
	EventKey  string `json:"event_key,omitempty"`
	DedupKey  string `json:"dedup_key,omitempty"`
	Artist    string `json:"artist_name,omitempty"`
	Venue     string `json:"venue,omitempty"`
	EventDate string `json:"date,omitempty"`
}

type APS struct {
	Alert Alert  `json:"alert"`
	Sound string `json:"sound,omitempty"`
	Badge *int   `json:"badge,omitempty"`
	// ThreadID groups notifications in Notification Center.
	ThreadID string `json:"thread-id,omitempty"`
}

type Alert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Error is an APNs rejection with its reason string.
type Error struct {
	StatusCode int
	Reason     string
}

func (e *Error) Error() string {
	return fmt.Sprintf("apns: %d %s", e.StatusCode, e.Reason)
}

// IsUnregistered reports whether the rejection means the token is
// permanently dead and should be retired rather than retried. 410 Gone is the
// app being uninstalled; BadDeviceToken is a token for a different
// environment or a malformed one. Neither improves with time.
func (e *Error) IsUnregistered() bool {
	return e.StatusCode == http.StatusGone ||
		e.Reason == "Unregistered" ||
		e.Reason == "BadDeviceToken"
}

// Send delivers one notification. A nil error means APNs accepted it — which
// is not a delivery guarantee, only that Apple took responsibility for it.
//
// The host comes from n.Environment, so one client reaches both. The Serves
// check is a guard rather than a live path — SendPushWorker filters devices
// on the same predicate before it gets here — and it fails rather than
// falling back for the reason hostFor does not default: the wrong host
// answers BadDeviceToken, which reads as a dead token and retires the device
// for good.
func (c *Client) Send(ctx context.Context, n Notification) error {
	host, err := hostFor(n.Environment)
	if err != nil {
		return err
	}
	if !c.Serves(n.Environment) {
		return fmt.Errorf("push: this deployment's key is not authorized for the %s environment (serves %s)",
			n.Environment, strings.Join(c.Environments(), ", "))
	}
	tok, err := c.authToken()
	if err != nil {
		return err
	}
	body, err := json.Marshal(n.Payload)
	if err != nil {
		return err
	}
	url := host + "/3/device/" + n.DeviceToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("apns-topic", c.BundleID)
	req.Header.Set("apns-push-type", "alert")
	// 5 = deliver at a time that conserves power. 10 = immediately. A new
	// concert announcement is not urgent enough to justify waking the device.
	req.Header.Set("apns-priority", "5")
	if n.CollapseID != "" {
		req.Header.Set("apns-collapse-id", n.CollapseID)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	// Errors arrive as {"reason":"..."} with the detail in the body, not the
	// status line. Decoding it is the difference between "retire this token"
	// and "retry later".
	var apnsErr struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&apnsErr)
	return &Error{StatusCode: resp.StatusCode, Reason: apnsErr.Reason}
}
