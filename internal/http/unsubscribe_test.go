package http

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUnsubscribeTokenRoundTrip(t *testing.T) {
	h := &UnsubscribeHandler{Secret: []byte("test-signing-key-32-bytes-long!!")}
	uid := uuid.New()

	tok := h.Token(uid)
	got, ok := h.verify(tok)
	if !ok {
		t.Fatalf("verify failed on freshly-issued token")
	}
	if got != uid {
		t.Fatalf("uuid mismatch: got %s want %s", got, uid)
	}
}

func TestUnsubscribeTokenRejectsBadSignature(t *testing.T) {
	h := &UnsubscribeHandler{Secret: []byte("test-signing-key-32-bytes-long!!")}
	tok := h.Token(uuid.New())
	// Flip one byte in the signature half.
	tampered := tok[:len(tok)-1] + string(rune('A'^tok[len(tok)-1]))
	if _, ok := h.verify(tampered); ok {
		t.Fatalf("verify accepted a tampered signature")
	}
}

func TestUnsubscribeTokenRejectsDifferentSecret(t *testing.T) {
	h1 := &UnsubscribeHandler{Secret: []byte("secret-A-32-bytes-long-pad-pad-!")}
	h2 := &UnsubscribeHandler{Secret: []byte("secret-B-32-bytes-long-pad-pad-!")}
	tok := h1.Token(uuid.New())
	if _, ok := h2.verify(tok); ok {
		t.Fatalf("verify accepted a token signed with a different secret")
	}
}

func TestUnsubscribeTokenExpires(t *testing.T) {
	h := &UnsubscribeHandler{Secret: []byte("test-signing-key-32-bytes-long!!")}
	uid := uuid.New()
	// Craft a payload with a very old timestamp by manipulating the Token
	// call — we can't easily backdate through the public API, but we can
	// forge a payload directly.
	id, _ := uid.MarshalBinary()
	oldTs := time.Now().Add(-2 * UnsubscribeTokenMaxAge).Unix()
	var ts [8]byte
	ts[0] = byte(oldTs >> 56)
	ts[1] = byte(oldTs >> 48)
	ts[2] = byte(oldTs >> 40)
	ts[3] = byte(oldTs >> 32)
	ts[4] = byte(oldTs >> 24)
	ts[5] = byte(oldTs >> 16)
	ts[6] = byte(oldTs >> 8)
	ts[7] = byte(oldTs)
	payload := append(append([]byte{}, id...), ts[:]...)
	// Sign it with the real HMAC so only the timestamp is out of date.
	tok := encodeTokenForTest(h.Secret, payload)
	if _, ok := h.verify(tok); ok {
		t.Fatalf("verify accepted a token older than UnsubscribeTokenMaxAge")
	}
}
