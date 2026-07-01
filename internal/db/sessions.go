package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Session struct {
	ID         string
	UserID     uuid.UUID
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

func CreateSession(ctx context.Context, pool *pgxpool.Pool, s Session) error {
	const q = `INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`
	_, err := pool.Exec(ctx, q, s.ID, s.UserID, s.ExpiresAt)
	return err
}

// GetSessionByID returns the session or ErrNoRows if missing/expired.
func GetSessionByID(ctx context.Context, pool *pgxpool.Pool, id string) (Session, error) {
	const q = `SELECT id, user_id, created_at, last_seen_at, expires_at FROM sessions WHERE id = $1 AND expires_at > now()`
	var s Session
	err := pool.QueryRow(ctx, q, id).Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt)
	return s, err
}

func TouchSession(ctx context.Context, pool *pgxpool.Pool, id string) error {
	const q = `UPDATE sessions SET last_seen_at = now() WHERE id = $1`
	_, err := pool.Exec(ctx, q, id)
	return err
}

func DeleteSession(ctx context.Context, pool *pgxpool.Pool, id string) error {
	const q = `DELETE FROM sessions WHERE id = $1`
	_, err := pool.Exec(ctx, q, id)
	return err
}
