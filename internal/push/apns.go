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

// jwtLifetime is how long a generated authentication token is reused.
//
// Apple rejects tokens older than 1 hour (ExpiredProviderToken) and also
// rejects a provider that mints them too often (TooManyProviderTokenUpdates),
// so this is bounded on both sides. Fifty minutes leaves room for clock skew
// against the first limit without approaching the second.
const jwtLifetime = 50 * time.Minute

// Client sends notifications to APNs.
type Client struct {
	// HTTPClient must speak HTTP/2. The zero value of http.Client does over
	// TLS, which is the only transport APNs offers.
	HTTPClient *http.Client
	Host       string
	KeyID      string
	TeamID     string
	BundleID   string

	signingKey *ecdsa.PrivateKey

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// Config is the deployment's APNs settings, sourced from the environment.
type Config struct {
	KeyID       string
	TeamID      string
	BundleID    string
	P8Key       string // PEM contents of the .p8, not a path
	Environment string // "sandbox" | "production"
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
	host := HostProduction
	if cfg.Environment == "sandbox" {
		host = HostSandbox
	}
	return &Client{
		// Timeout covers the whole request. APNs is fast; a stalled
		// connection here would otherwise hold a river worker slot.
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		Host:       host,
		KeyID:      cfg.KeyID,
		TeamID:     cfg.TeamID,
		BundleID:   cfg.BundleID,
		signingKey: key,
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
func (c *Client) Send(ctx context.Context, n Notification) error {
	tok, err := c.authToken()
	if err != nil {
		return err
	}
	body, err := json.Marshal(n.Payload)
	if err != nil {
		return err
	}
	url := c.Host + "/3/device/" + n.DeviceToken
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
