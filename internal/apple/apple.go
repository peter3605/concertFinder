// Package apple is the Sign in with Apple client (CF-35, docs/public-path.md
// section 4.1): verifying identity tokens against Apple's published keys,
// exchanging authorization codes for refresh tokens, and revoking those
// tokens when an account is deleted.
//
// It owns its own types, like every other external API here. No JWT library:
// the identity token is RS256 and the client secret is ES256, and both are a
// few lines of the standard library -- internal/push already signs ES256 the
// same way.
package apple

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Issuer is the iss of every identity token Apple signs, and the aud of the
// client secret we sign for Apple. It is fixed, unlike BaseURL, so a test
// server standing in for Apple cannot also change what counts as Apple.
const Issuer = "https://appleid.apple.com"

const (
	defaultBaseURL = "https://appleid.apple.com"
	// keysTTL bounds how long a fetched key set is trusted. Apple rotates
	// rarely; an unknown kid triggers a refetch sooner (keysRefetchMin).
	keysTTL        = time.Hour
	keysRefetchMin = time.Minute
	// clockSkew tolerates a server clock slightly behind Apple's on exp and
	// iat. Identity tokens live ten minutes, so this is small by comparison.
	clockSkew = 30 * time.Second
	// clientSecretLifetime is far below Apple's six-month ceiling: a secret
	// is minted per call, so it only has to outlive one request.
	clientSecretLifetime = 5 * time.Minute
)

// Config is what New needs. TeamID, KeyID and P8Key are the Sign in with
// Apple key from CF-34 (APPLE_TEAM_ID, APPLE_SIGNIN_KEY_ID,
// APPLE_SIGNIN_P8_KEY).
type Config struct {
	TeamID string
	KeyID  string
	// P8Key is the PEM of the .p8. Escaped newlines (a literal backslash-n,
	// which is how it is stored in SSM) are unescaped before decoding.
	P8Key string
	// BaseURL is for tests; empty is https://appleid.apple.com.
	BaseURL    string
	HTTPClient *http.Client
	// Now is for tests; nil is time.Now.
	Now func() time.Time
}

// Client talks to appleid.apple.com. Safe for concurrent use.
type Client struct {
	teamID     string
	keyID      string
	signingKey *ecdsa.PrivateKey
	baseURL    string
	http       *http.Client
	now        func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	keysAt    time.Time
	keysTried time.Time
}

// New builds a Client, refusing a key that does not parse so a bad secret
// fails at startup instead of at somebody's first sign-in.
func New(cfg Config) (*Client, error) {
	if cfg.TeamID == "" || cfg.KeyID == "" || strings.TrimSpace(cfg.P8Key) == "" {
		return nil, errors.New("apple: team ID, key ID and .p8 key are all required")
	}
	key, err := parseP8(cfg.P8Key)
	if err != nil {
		return nil, err
	}
	c := &Client{
		teamID:     cfg.TeamID,
		keyID:      cfg.KeyID,
		signingKey: key,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		http:       cfg.HTTPClient,
		now:        cfg.Now,
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 10 * time.Second}
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// parseP8 decodes the PKCS#8 PEM Apple issues. Same shape as push.parseP8,
// deliberately duplicated: each external API owns its own client.
func parseP8(pemData string) (*ecdsa.PrivateKey, error) {
	pemData = strings.ReplaceAll(pemData, `\n`, "\n")
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("apple: APPLE_SIGNIN_P8_KEY is not valid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apple: parse p8: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apple: expected an ECDSA key, got %T", parsed)
	}
	return key, nil
}

// HashNonce is what the nonce claim of an identity token must equal: the
// hex SHA-256 of the raw nonce. The raw value never goes to Apple. The web
// flow sends this hash as the authorize request's nonce, and the iOS app sets
// it as ASAuthorizationAppleIDRequest.nonce, so one rule checks both.
func HashNonce(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Claims are the parts of a verified identity token this app uses.
type Claims struct {
	// Subject is Apple's stable user identifier for this team -- the same
	// value whether the token came from the web or the app, which is what
	// lets one account sign in from both.
	Subject string
	// Email is present when the user granted it; it may be a private relay
	// address. Unlike the name, Apple repeats it in later tokens.
	Email          string
	EmailVerified  bool
	IsPrivateEmail bool
}

// Verification failures. Each is distinct so the tests can prove every
// check is made; callers treat them all as one refusal.
var (
	ErrMalformed    = errors.New("apple: identity token is malformed")
	ErrAlgorithm    = errors.New("apple: identity token is not RS256")
	ErrUnknownKey   = errors.New("apple: identity token is signed by an unknown key")
	ErrSignature    = errors.New("apple: identity token signature is invalid")
	ErrIssuer       = errors.New("apple: identity token issuer is not Apple")
	ErrAudience     = errors.New("apple: identity token was issued to a different client")
	ErrExpired      = errors.New("apple: identity token has expired")
	ErrNonce        = errors.New("apple: identity token nonce does not match")
	ErrNoSubject    = errors.New("apple: identity token has no subject")
	ErrIssuedFuture = errors.New("apple: identity token is issued in the future")
)

type tokenHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type tokenClaims struct {
	Iss            string          `json:"iss"`
	Aud            json.RawMessage `json:"aud"`
	Exp            int64           `json:"exp"`
	Iat            int64           `json:"iat"`
	Sub            string          `json:"sub"`
	Nonce          string          `json:"nonce"`
	Email          string          `json:"email"`
	EmailVerified  flexBool        `json:"email_verified"`
	IsPrivateEmail flexBool        `json:"is_private_email"`
}

// flexBool reads Apple's booleans, which arrive as either true or "true".
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = flexBool(s == "true")
	return nil
}

// VerifyIdentityToken checks an identity token's signature against Apple's
// published keys, then iss, aud, exp and nonce. audience is the client the
// token must have been issued to -- the Services ID on the web callback, the
// bundle ID on the native route -- and accepting only that one per route is
// what stops a token minted for one client signing in through the other.
// rawNonce is the value this server (web) or the app (native) generated; it
// must be non-empty, because a token with no nonce check is replayable.
func (c *Client) VerifyIdentityToken(ctx context.Context, token, audience, rawNonce string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrMalformed
	}
	enc := base64.RawURLEncoding
	hb, err := enc.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var h tokenHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return Claims{}, ErrMalformed
	}
	// The algorithm is pinned, never taken from the header: "none" and an
	// HS256 token keyed with the public key are the classic JWT forgeries.
	if h.Alg != "RS256" {
		return Claims{}, ErrAlgorithm
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	pub, err := c.key(ctx, h.Kid)
	if err != nil {
		return Claims{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return Claims{}, ErrSignature
	}

	cb, err := enc.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var tc tokenClaims
	if err := json.Unmarshal(cb, &tc); err != nil {
		return Claims{}, ErrMalformed
	}
	if tc.Iss != Issuer {
		return Claims{}, ErrIssuer
	}
	if audience == "" || !audienceMatches(tc.Aud, audience) {
		return Claims{}, ErrAudience
	}
	now := c.now()
	if tc.Exp == 0 || now.After(time.Unix(tc.Exp, 0).Add(clockSkew)) {
		return Claims{}, ErrExpired
	}
	if tc.Iat != 0 && time.Unix(tc.Iat, 0).After(now.Add(clockSkew)) {
		return Claims{}, ErrIssuedFuture
	}
	if rawNonce == "" || tc.Nonce == "" ||
		subtle.ConstantTimeCompare([]byte(tc.Nonce), []byte(HashNonce(rawNonce))) != 1 {
		return Claims{}, ErrNonce
	}
	if tc.Sub == "" {
		return Claims{}, ErrNoSubject
	}
	return Claims{
		Subject:        tc.Sub,
		Email:          strings.ToLower(strings.TrimSpace(tc.Email)),
		EmailVerified:  bool(tc.EmailVerified),
		IsPrivateEmail: bool(tc.IsPrivateEmail),
	}, nil
}

// audienceMatches accepts aud as a string (what Apple sends) or an array
// (what RFC 7519 allows).
func audienceMatches(raw json.RawMessage, want string) bool {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return one == want
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// key returns Apple's public key for kid, fetching the key set when it is
// stale or does not hold kid. An unknown kid refetches at most once per
// keysRefetchMin, so a stream of garbage tokens cannot turn into a stream of
// requests to Apple.
func (c *Client) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if k, ok := c.keys[kid]; ok && now.Sub(c.keysAt) < keysTTL {
		return k, nil
	}
	if now.Sub(c.keysTried) < keysRefetchMin && c.keys != nil {
		if k, ok := c.keys[kid]; ok {
			return k, nil
		}
		return nil, ErrUnknownKey
	}
	c.keysTried = now
	keys, err := c.fetchKeys(ctx)
	if err != nil {
		// Keep serving a stale set rather than failing every sign-in while
		// Apple's endpoint is briefly unreachable.
		if k, ok := c.keys[kid]; ok {
			return k, nil
		}
		return nil, err
	}
	c.keys, c.keysAt = keys, now
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, ErrUnknownKey
}

func (c *Client) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/auth/keys", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apple: fetch keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apple: fetch keys: status %d", resp.StatusCode)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("apple: decode keys: %w", err)
	}
	out := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil || len(eb) == 0 || len(eb) > 4 {
			continue
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}
	}
	if len(out) == 0 {
		return nil, errors.New("apple: key set holds no RSA keys")
	}
	return out, nil
}

// clientSecret signs the ES256 JWT Apple takes as client_secret. sub is the
// client_id of the request, and it differs by route: the Services ID for the
// web, the bundle ID for the app. A refresh token can only be revoked with the
// client_id that obtained it, which is why that is stored beside it.
func (c *Client) clientSecret(clientID string) (string, error) {
	now := c.now()
	header := map[string]string{"alg": "ES256", "kid": c.keyID}
	claims := map[string]any{
		"iss": c.teamID,
		"iat": now.Unix(),
		"exp": now.Add(clientSecretLifetime).Unix(),
		"aud": Issuer,
		"sub": clientID,
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, c.signingKey, sum[:])
	if err != nil {
		return "", fmt.Errorf("apple: sign client secret: %w", err)
	}
	// JWS ES256 is r||s, each left-padded to the curve size, not ASN.1.
	size := (c.signingKey.Curve.Params().BitSize + 7) / 8
	sig := make([]byte, 2*size)
	r.FillBytes(sig[:size])
	s.FillBytes(sig[size:])
	return input + "." + enc.EncodeToString(sig), nil
}

// TokenResponse is Apple's answer to a code exchange.
type TokenResponse struct {
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

// Error is an error response from Apple's token or revoke endpoint.
type Error struct {
	Status int
	Code   string // e.g. invalid_grant, invalid_client
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("apple: %s (status %d)", e.Code, e.Status)
	}
	return fmt.Sprintf("apple: status %d", e.Status)
}

// IsInvalidGrant reports whether Apple said the code or token is not valid.
// On revoke that means there is nothing left to revoke.
func IsInvalidGrant(err error) bool {
	var ae *Error
	return errors.As(err, &ae) && ae.Code == "invalid_grant"
}

// RedeemCode trades an authorization code for tokens. redirectURI must be
// the one the authorize request used on the web, and empty for a code from
// the native app, which has none.
//
// Not named ExchangeCode: that is auth.ExchangeCode, the Spotify token
// source internal/auth/spotify_gate_test.go confines to one file by name.
func (c *Client) RedeemCode(ctx context.Context, clientID, code, redirectURI string) (TokenResponse, error) {
	secret, err := c.clientSecret(clientID)
	if err != nil {
		return TokenResponse{}, err
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {secret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
	}
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	var out TokenResponse
	if err := c.post(ctx, "/auth/token", form, &out); err != nil {
		return TokenResponse{}, err
	}
	if out.RefreshToken == "" || out.IDToken == "" {
		return TokenResponse{}, errors.New("apple: token response is missing a refresh or identity token")
	}
	return out, nil
}

// Revoke invalidates a refresh token, which is what removes this app from
// the user's "Apps Using Apple ID" list. clientID must be the one that
// obtained the token. A nil error means revoked; callers should also treat
// IsInvalidGrant as done, since a token Apple no longer knows has nothing left
// to revoke.
func (c *Client) Revoke(ctx context.Context, clientID, refreshToken string) error {
	secret, err := c.clientSecret(clientID)
	if err != nil {
		return err
	}
	form := url.Values{
		"client_id":       {clientID},
		"client_secret":   {secret},
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
	}
	return c.post(ctx, "/auth/revoke", form, nil)
}

func (c *Client) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apple: %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return &Error{Status: resp.StatusCode, Code: e.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("apple: decode %s: %w", path, err)
	}
	return nil
}

// UserField is the JSON Apple form-posts as "user" on the web callback --
// only on the first authorization, which is why it is stored immediately.
type UserField struct {
	Name struct {
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
	} `json:"name"`
	Email string `json:"email"`
}

// DisplayName joins the given and family names, either of which may be
// empty.
func DisplayName(given, family string) string {
	return strings.TrimSpace(strings.TrimSpace(given) + " " + strings.TrimSpace(family))
}

// ParseUserField reads the web callback's user field; malformed or absent
// yields the zero value, never an error -- a missing name must not fail a
// sign-in.
func ParseUserField(s string) UserField {
	var u UserField
	if s != "" {
		_ = json.Unmarshal([]byte(s), &u)
	}
	return u
}

// SubjectOf reads the sub of an identity token WITHOUT verifying it. It is
// only for the id_token in a TokenResponse, which this server just received
// from Apple's token endpoint over TLS; anything a client presents goes
// through VerifyIdentityToken. Callers use it to require that the code they
// exchanged belongs to the same user as the verified token, so a code from
// somebody else's authorization cannot attach their refresh token to this
// account.
func SubjectOf(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(cb, &c) != nil {
		return ""
	}
	return c.Sub
}
