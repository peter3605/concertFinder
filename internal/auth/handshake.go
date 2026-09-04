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
	// InviteCode is the admission code presented at /login, redeemed at
	// /callback and only when the login would create a new user.
	InviteCode string
	ExpiresAt  time.Time
}

// HandshakeStore abstracts handshake persistence. Only the DB-backed
// implementation below remains — an in-memory variant existed for
// single-instance dev but was never wired up, and it would have broken the
// moment /login and /callback landed on different replicas.
// PendingHandshake is what /login hands the store. It is a struct rather
// than a positional argument list because it now carries two independently
// optional strings -- AppChallenge and InviteCode -- and adjacent optional
// strings of the same type are swappable at a call site without the compiler
// noticing. The failure would be a mobile login redeeming an invite as its
// PKCE challenge, which is silent.
type PendingHandshake struct {
	Key          string
	Verifier     string
	State        string
	AppChallenge string
	InviteCode   string
}

type HandshakeStore interface {
	Put(ctx context.Context, h PendingHandshake, ttl time.Duration) error
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

func (s *DBHandshakeStore) Put(ctx context.Context, h PendingHandshake, ttl time.Duration) error {
	return db.PutHandshake(ctx, s.Pool, db.OAuthHandshake{
		Key:          h.Key,
		Verifier:     h.Verifier,
		State:        h.State,
		AppChallenge: h.AppChallenge,
		InviteCode:   h.InviteCode,
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
		InviteCode:   h.InviteCode,
		ExpiresAt:    h.ExpiresAt,
	}, true
}
