package auth

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// handshake holds the transient state of an in-flight OAuth login: the PKCE
// verifier and CSRF state, keyed by a short-lived cookie set at /login and
// consumed at /callback.
type handshake struct {
	Verifier  string
	State     string
	ExpiresAt time.Time
}

// HandshakeStore is the interface both the in-memory and DB-backed stores
// satisfy. Multi-instance deploys need the DB variant.
type HandshakeStore interface {
	Put(ctx context.Context, key, verifier, state string, ttl time.Duration) error
	Take(ctx context.Context, key string) (*handshake, bool)
}

// --- In-memory implementation ---

// MemHandshakeStore is a single-process TTL map. Fine for dev / single-instance.
type MemHandshakeStore struct {
	mu   sync.Mutex
	data map[string]handshake
}

func NewMemHandshakeStore() *MemHandshakeStore {
	return &MemHandshakeStore{data: map[string]handshake{}}
}

func (s *MemHandshakeStore) Put(_ context.Context, key, verifier, state string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = handshake{Verifier: verifier, State: state, ExpiresAt: time.Now().Add(ttl)}
	s.gcLocked()
	return nil
}

func (s *MemHandshakeStore) Take(_ context.Context, key string) (*handshake, bool) {
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

func (s *MemHandshakeStore) gcLocked() {
	now := time.Now()
	for k, v := range s.data {
		if now.After(v.ExpiresAt) {
			delete(s.data, k)
		}
	}
}

// --- DB-backed implementation ---

// DBHandshakeStore persists handshakes to oauth_handshakes so /login and
// /callback can land on different replicas.
type DBHandshakeStore struct {
	Pool *pgxpool.Pool
}

func NewDBHandshakeStore(pool *pgxpool.Pool) *DBHandshakeStore {
	return &DBHandshakeStore{Pool: pool}
}

func (s *DBHandshakeStore) Put(ctx context.Context, key, verifier, state string, ttl time.Duration) error {
	return db.PutHandshake(ctx, s.Pool, db.OAuthHandshake{
		Key:       key,
		Verifier:  verifier,
		State:     state,
		ExpiresAt: time.Now().Add(ttl),
	})
}

func (s *DBHandshakeStore) Take(ctx context.Context, key string) (*handshake, bool) {
	h, hit, err := db.TakeHandshake(ctx, s.Pool, key)
	if err != nil || !hit {
		return nil, false
	}
	return &handshake{Verifier: h.Verifier, State: h.State, ExpiresAt: h.ExpiresAt}, true
}
