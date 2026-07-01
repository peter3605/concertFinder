package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

const verifierBytes = 64

// GenerateVerifier returns a fresh PKCE code verifier (base64url, no padding).
// Length is well above the 43-byte RFC 7636 minimum.
func GenerateVerifier() (string, error) {
	b := make([]byte, verifierBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("verifier entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ChallengeFromVerifier computes the S256 code challenge for a verifier.
func ChallengeFromVerifier(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomString returns n random bytes as a base64url (no padding) string.
// Used for state values and session IDs.
func RandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
