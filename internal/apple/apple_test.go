package apple

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/apple/appletest"
)

const (
	testServices = "com.example.app.web"
	testBundle   = "com.example.app"
	testNonce    = "raw-nonce-value"
)

var testNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func newTestClient(t *testing.T, srv *appletest.Server) *Client {
	t.Helper()
	c, err := New(Config{
		TeamID:  "TEAM123",
		KeyID:   "KEY123",
		P8Key:   srv.EscapedP8,
		BaseURL: srv.URL,
		Now:     func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func goodClaims() map[string]any {
	return map[string]any{
		"iss":            Issuer,
		"aud":            testServices,
		"exp":            testNow.Add(10 * time.Minute).Unix(),
		"iat":            testNow.Unix(),
		"sub":            "001234.abcdef",
		"nonce":          HashNonce(testNonce),
		"email":          "Someone@privaterelay.appleid.com",
		"email_verified": "true",
	}
}

func TestVerifyAcceptsAGoodToken(t *testing.T) {
	srv := appletest.NewServer(t)
	c := newTestClient(t, srv)
	got, err := c.VerifyIdentityToken(context.Background(), srv.Sign(goodClaims()), testServices, testNonce)
	if err != nil {
		t.Fatalf("good token refused: %v", err)
	}
	if got.Subject != "001234.abcdef" || got.Email != "someone@privaterelay.appleid.com" || !got.EmailVerified {
		t.Fatalf("claims = %+v", got)
	}
}

// One test per check, so dropping any single check fails CI.
func TestVerifyRejections(t *testing.T) {
	srv := appletest.NewServer(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)

	cases := []struct {
		name     string
		token    func() string
		audience string
		nonce    string
		want     error
	}{
		{"bad signature", func() string {
			tok := srv.Sign(goodClaims())
			parts := strings.Split(tok, ".")
			c := goodClaims()
			c["sub"] = "someone-else"
			cb, _ := json.Marshal(c)
			return parts[0] + "." + base64.RawURLEncoding.EncodeToString(cb) + "." + parts[2]
		}, testServices, testNonce, ErrSignature},
		{"signed by a key that is not Apple's", func() string {
			return signWith(other, srv.Kid, "RS256", goodClaims())
		}, testServices, testNonce, ErrSignature},
		{"unknown kid", func() string {
			return signWith(srv.Key, "not-a-kid", "RS256", goodClaims())
		}, testServices, testNonce, ErrUnknownKey},
		{"alg none", func() string {
			hb, _ := json.Marshal(map[string]string{"alg": "none", "kid": srv.Kid})
			cb, _ := json.Marshal(goodClaims())
			return base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb) + "."
		}, testServices, testNonce, ErrAlgorithm},
		{"wrong issuer", func() string {
			c := goodClaims()
			c["iss"] = "https://evil.example"
			return srv.Sign(c)
		}, testServices, testNonce, ErrIssuer},
		{"web token on the native route", func() string {
			return srv.Sign(goodClaims()) // aud = Services ID
		}, testBundle, testNonce, ErrAudience},
		{"native token on the web route", func() string {
			c := goodClaims()
			c["aud"] = testBundle
			return srv.Sign(c)
		}, testServices, testNonce, ErrAudience},
		{"expired", func() string {
			c := goodClaims()
			c["exp"] = testNow.Add(-time.Hour).Unix()
			return srv.Sign(c)
		}, testServices, testNonce, ErrExpired},
		{"no exp", func() string {
			c := goodClaims()
			delete(c, "exp")
			return srv.Sign(c)
		}, testServices, testNonce, ErrExpired},
		{"issued in the future", func() string {
			c := goodClaims()
			c["iat"] = testNow.Add(time.Hour).Unix()
			return srv.Sign(c)
		}, testServices, testNonce, ErrIssuedFuture},
		{"nonce mismatch", func() string {
			return srv.Sign(goodClaims())
		}, testServices, "a-different-nonce", ErrNonce},
		{"raw nonce sent instead of its hash", func() string {
			c := goodClaims()
			c["nonce"] = testNonce
			return srv.Sign(c)
		}, testServices, testNonce, ErrNonce},
		{"token carries no nonce", func() string {
			c := goodClaims()
			delete(c, "nonce")
			return srv.Sign(c)
		}, testServices, testNonce, ErrNonce},
		{"caller has no nonce", func() string {
			return srv.Sign(goodClaims())
		}, testServices, "", ErrNonce},
		{"no subject", func() string {
			c := goodClaims()
			delete(c, "sub")
			return srv.Sign(c)
		}, testServices, testNonce, ErrNoSubject},
		{"not a JWT", func() string { return "abc.def" }, testServices, testNonce, ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, srv)
			_, err := c.VerifyIdentityToken(context.Background(), tc.token(), tc.audience, tc.nonce)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// An unknown kid must not become one request to Apple per garbage token.
func TestUnknownKidRefetchIsRateLimited(t *testing.T) {
	srv := appletest.NewServer(t)
	c := newTestClient(t, srv)
	for i := 0; i < 5; i++ {
		_, _ = c.VerifyIdentityToken(context.Background(),
			signWith(srv.Key, "nope", "RS256", goodClaims()), testServices, testNonce)
	}
	if n := srv.KeyFetches.Load(); n != 1 {
		t.Fatalf("key set fetched %d times for five unknown-kid tokens, want 1", n)
	}
}

func TestEscapedP8IsAccepted(t *testing.T) {
	srv := appletest.NewServer(t)
	if !strings.Contains(srv.EscapedP8, `\n`) || strings.Contains(srv.EscapedP8, "\n") {
		t.Fatal("fixture should be the SSM single-line form")
	}
	newTestClient(t, srv) // fatal on failure
}

// The client secret's sub must be the client_id of the call: a refresh
// token can only be revoked by the client that obtained it.
func TestClientSecretShapeAndSignature(t *testing.T) {
	srv := appletest.NewServer(t)
	c := newTestClient(t, srv)
	secret, err := c.clientSecret(testBundle)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(secret, ".")
	if len(parts) != 3 {
		t.Fatalf("secret = %q", secret)
	}
	var h map[string]string
	var cl map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(hb, &h)
	_ = json.Unmarshal(cb, &cl)
	if h["alg"] != "ES256" || h["kid"] != "KEY123" {
		t.Fatalf("header = %v", h)
	}
	if cl["iss"] != "TEAM123" || cl["sub"] != testBundle || cl["aud"] != Issuer {
		t.Fatalf("claims = %v", cl)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("ES256 signature is %d bytes, want 64 (r||s, not ASN.1)", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&srv.P8.PublicKey, sum[:], r, s) {
		t.Fatal("client secret does not verify against the .p8's public key")
	}
}

func TestExchangeAndRevoke(t *testing.T) {
	srv := appletest.NewServer(t)
	c := newTestClient(t, srv)
	srv.IDTokenForCode = func(form url.Values) string { return srv.Sign(goodClaims()) }

	tok, err := c.RedeemCode(context.Background(), testServices, "the-code", "https://x/cb")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken == "" || tok.IDToken == "" {
		t.Fatalf("tokens = %+v", tok)
	}
	last := srv.LastTokenForm()
	if last.Get("client_id") != testServices || last.Get("redirect_uri") != "https://x/cb" || last.Get("code") != "the-code" {
		t.Fatalf("token form = %v", last)
	}

	if err := c.Revoke(context.Background(), testServices, tok.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if got := srv.Revoked(); len(got) != 1 || got[0] != tok.RefreshToken {
		t.Fatalf("revoked = %v", got)
	}

	srv.RevokeStatus.Store(400)
	srv.RevokeError.Store("invalid_grant")
	if err := c.Revoke(context.Background(), testServices, "x"); !IsInvalidGrant(err) {
		t.Fatalf("err = %v, want invalid_grant", err)
	}
	srv.RevokeStatus.Store(503)
	srv.RevokeError.Store("")
	if err := c.Revoke(context.Background(), testServices, "x"); err == nil || IsInvalidGrant(err) {
		t.Fatalf("a 503 must be a retryable error, got %v", err)
	}
}

func TestParseUserField(t *testing.T) {
	u := ParseUserField(`{"name":{"firstName":"Ada","lastName":"Lovelace"},"email":"a@b.c"}`)
	if DisplayName(u.Name.FirstName, u.Name.LastName) != "Ada Lovelace" {
		t.Fatalf("got %+v", u)
	}
	if ParseUserField("not json").Name.FirstName != "" || ParseUserField("").Email != "" {
		t.Fatal("malformed input should yield zero")
	}
	if DisplayName("", "Lovelace") != "Lovelace" {
		t.Fatal("one name alone")
	}
}

func signWith(key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	hb, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid})
	cb, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sum := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}
