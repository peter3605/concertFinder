package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// handshake holds the transient state of an in-flight OAuth login: the PKCE
// verifier and CSRF state, keyed by a short-lived cookie set at /login and
// consumed at /callback.
type handshake struct {
	Verifier string
	State    string
	// AppChallenge is non-empty only for app-initiated logins; see
	// db.OAuthHandshake.AppChallenge.
	AppChallenge string
	ExpiresAt    time.Time
}

// HandshakeStore abstracts handshake persistence. Only the DB-backed
// implementation below remains — an in-memory variant existed for
// single-instance dev but was never wired up, and it would have broken the
// moment /login and /callback landed on different replicas.
type HandshakeStore interface {
	Put(ctx context.Context, key, verifier, state, appChallenge string, ttl time.Duration) error
	Take(ctx context.Context, key string) (*handshake, bool)
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

func (s *DBHandshakeStore) Put(ctx context.Context, key, verifier, state, appChallenge string, ttl time.Duration) error {
	return db.PutHandshake(ctx, s.Pool, db.OAuthHandshake{
		Key:          key,
		Verifier:     verifier,
		State:        state,
		AppChallenge: appChallenge,
		ExpiresAt:    time.Now().Add(ttl),
	})
}

func (s *DBHandshakeStore) Take(ctx context.Context, key string) (*handshake, bool) {
	h, hit, err := db.TakeHandshake(ctx, s.Pool, key)
	if err != nil || !hit {
		return nil, false
	}
	return &handshake{
		Verifier:     h.Verifier,
		State:        h.State,
		AppChallenge: h.AppChallenge,
		ExpiresAt:    h.ExpiresAt,
	}, true
}
