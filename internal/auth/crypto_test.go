package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeKey(t *testing.T) {
	good := hex.EncodeToString(make([]byte, 32))
	if _, err := DecodeKey(good); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if _, err := DecodeKey("not-hex"); err == nil {
		t.Fatal("expected error for non-hex key")
	}
	if _, err := DecodeKey(hex.EncodeToString(make([]byte, 16))); err == nil {
		t.Fatal("expected error for wrong-length key")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("BQD...refresh-token-goes-here")

	ct1, nonce1, err := EncryptToken(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	ct2, nonce2, err := EncryptToken(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(nonce1, nonce2) {
		t.Fatal("nonce must be unique per encryption")
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("ciphertext must differ across encryptions")
	}

	got, err := DecryptToken(key, ct1, nonce1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("roundtrip mismatch: got %q, want %q", got, plaintext)
	}

	ct1[0] ^= 0xff
	if _, err := DecryptToken(key, ct1, nonce1); err == nil {
		t.Fatal("expected auth failure on tampered ciphertext")
	}
}

// The reason AccessTokenFor checks for an empty credential itself instead of
// letting DecryptToken report it.
//
// A disconnected account holds a zero-length token and nonce (the column is
// NOT NULL, so db.DisconnectSpotify zeroes rather than NULLs). Handed those,
// GCM does not return an error — it **panics**, because Open panics whenever
// the nonce is not exactly NonceSize bytes. That is the whole reason the guard
// in AccessTokenFor exists: without it the first background job or API call
// for a disconnected user takes down its goroutine rather than reporting an
// ordinary, user-initiated state.
//
// If this ever stops panicking the guard can be reconsidered. Pinning it here
// means that change is a failing test rather than a silent one.
func TestDecryptingAnEmptyCredentialPanics(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("DecryptToken no longer panics on an empty credential; AccessTokenFor's guard can be revisited")
		}
		if !strings.Contains(fmt.Sprint(r), "nonce") {
			t.Errorf("panicked for an unexpected reason: %v", r)
		}
	}()

	_, _ = DecryptToken(key, nil, nil)
}
