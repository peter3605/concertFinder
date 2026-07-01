package auth

import (
	"sync"
	"time"
)

// handshake holds the transient state of an in-flight OAuth login: the PKCE
// verifier and CSRF state, keyed by a short-lived cookie set at /login and
// consumed at /callback.
type handshake struct {
	Verifier  string
	State     string
	ExpiresAt time.Time
}

// HandshakeStore is an in-memory TTL map. Phase 1 is single-instance so this is
// sufficient; a shared deployment would move this to Redis or Postgres.
type HandshakeStore struct {
	mu   sync.Mutex
	data map[string]handshake
}

func NewHandshakeStore() *HandshakeStore {
	return &HandshakeStore{data: map[string]handshake{}}
}

func (s *HandshakeStore) Put(key, verifier, state string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = handshake{Verifier: verifier, State: state, ExpiresAt: time.Now().Add(ttl)}
	s.gcLocked()
}

// Take atomically returns and removes the handshake, or (nil, false) if missing/expired.
func (s *HandshakeStore) Take(key string) (*handshake, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.data[key]
	if !ok {
		return nil, false
	}
	delete(s.data, key)
	if time.Now().After(h.ExpiresAt) {
		return nil, false
	}
	return &h, true
}

func (s *HandshakeStore) gcLocked() {
	now := time.Now()
	for k, v := range s.data {
		if now.After(v.ExpiresAt) {
			delete(s.data, k)
		}
	}
}
