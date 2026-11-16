// Package appletest stands in for appleid.apple.com in tests: a key set, an
// identity-token signer, and the token and revoke endpoints. It does not
// import package apple, so apple's own tests can use it.
package appletest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Server is a fake Apple. Its zero-configured behaviour answers every code
// with a refresh token and the identity token IDTokenForCode returns, and
// every revoke with 200.
type Server struct {
	URL string
	Kid string
	// Key signs identity tokens; its public half is in the key set.
	Key *rsa.PrivateKey
	// P8 is a Sign in with Apple .p8 key, and EscapedP8 its PEM in the
	// single-line, backslash-n form SSM stores.
	P8        *ecdsa.PrivateKey
	EscapedP8 string

	KeyFetches atomic.Int64
	// IDTokenForCode builds the id_token a code exchange returns.
	IDTokenForCode func(form url.Values) string
	// TokenStatus, when non-zero, fails the token endpoint with it.
	TokenStatus atomic.Int64
	// RevokeStatus and RevokeError, when set, fail the revoke endpoint.
	RevokeStatus atomic.Int64
	RevokeError  atomic.Value // string

	mu        sync.Mutex
	tokenForm url.Values
	revoked   []string
	revokeIDs []string
	refreshN  int
}

// NewServer starts a fake Apple, closed with the test.
func NewServer(t testing.TB) *Server {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ek)
	if err != nil {
		t.Fatal(err)
	}
	p8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	s := &Server{
		Kid:       "test-kid",
		Key:       rk,
		P8:        ek,
		EscapedP8: strings.ReplaceAll(strings.TrimSpace(p8), "\n", `\n`),
	}
	s.RevokeError.Store("")
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/keys", s.keys)
	mux.HandleFunc("/auth/token", s.token)
	mux.HandleFunc("/auth/revoke", s.revoke)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	s.URL = hs.URL
	return s
}

// Sign returns an RS256 identity token over claims, signed by Key.
func (s *Server) Sign(claims map[string]any) string {
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": s.Kid})
	cb, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sum := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, s.Key, crypto.SHA256, sum[:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// HashNonce mirrors apple.HashNonce, without importing it.
func HashNonce(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// LastTokenForm is the form of the most recent code exchange.
func (s *Server) LastTokenForm() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenForm
}

// Revoked lists the refresh tokens successfully revoked, in order.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

// RevokedClientIDs lists the client_id of each successful revoke.
func (s *Server) RevokedClientIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revokeIDs...)
}

func (s *Server) keys(w http.ResponseWriter, _ *http.Request) {
	s.KeyFetches.Add(1)
	pub := s.Key.PublicKey
	e := big.NewInt(int64(pub.E)).Bytes()
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": s.Kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(e),
	}}})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	s.tokenForm = r.PostForm
	s.refreshN++
	n := s.refreshN
	s.mu.Unlock()
	if st := s.TokenStatus.Load(); st != 0 {
		w.WriteHeader(int(st))
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	idt := ""
	if s.IDTokenForCode != nil {
		idt = s.IDTokenForCode(r.PostForm)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  "access",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "refresh-" + r.PostForm.Get("client_id") + "-" + itoa(n),
		"id_token":      idt,
	})
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if st := s.RevokeStatus.Load(); st != 0 {
		w.WriteHeader(int(st))
		if code, _ := s.RevokeError.Load().(string); code != "" {
			_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
		}
		return
	}
	s.mu.Lock()
	s.revoked = append(s.revoked, r.PostForm.Get("token"))
	s.revokeIDs = append(s.revokeIDs, r.PostForm.Get("client_id"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func itoa(n int) string {
	return big.NewInt(int64(n)).String()
}
