package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
