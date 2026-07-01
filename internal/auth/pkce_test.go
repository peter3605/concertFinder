package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestGenerateVerifier(t *testing.T) {
	v1, err := GenerateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	v2, err := GenerateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if v1 == v2 {
		t.Fatal("verifiers must differ")
	}
	if len(v1) < 43 {
		t.Fatalf("verifier too short: %d chars", len(v1))
	}
}

func TestChallengeMatchesRFC(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := ChallengeFromVerifier(verifier); got != want {
		t.Fatalf("challenge mismatch: got %q, want %q", got, want)
	}
}
