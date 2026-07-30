package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConcertSnapshot is the last completed concert scan for a (user, location).
// SWR handler returns snapshot directly and enqueues a background refresh
// when ComputedAt is older than the configured staleness window.
type ConcertSnapshot struct {
	UserID      uuid.UUID
	LocationKey string
	Snapshot    []byte // serialized []concerts.Concert
	ComputedAt  time.Time
}

// GetConcertSnapshot returns (snapshot, true, nil) on hit, (zero, false, nil) on miss.
func GetConcertSnapshot(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, locationKey string) (ConcertSnapshot, bool, error) {
	const q = `
SELECT user_id, location_key, snapshot, computed_at
FROM user_concert_snapshots
WHERE user_id = $1 AND location_key = $2
`
	var s ConcertSnapshot
	err := pool.QueryRow(ctx, q, userID, locationKey).Scan(&s.UserID, &s.LocationKey, &s.Snapshot, &s.ComputedAt)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return ConcertSnapshot{}, false, nil
		}
		return ConcertSnapshot{}, false, err
	}
	return s, true, nil
}

// UpsertConcertSnapshot stores or refreshes a snapshot row. computed_at is
// caller-supplied so the writer (the scan worker) can stamp it at the
// moment the scan finished rather than the moment the row was written.
func UpsertConcertSnapshot(ctx context.Context, pool *pgxpool.Pool, s ConcertSnapshot) error {
	const q = `
INSERT INTO user_concert_snapshots (user_id, location_key, snapshot, computed_at, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (user_id, location_key) DO UPDATE SET
  snapshot    = EXCLUDED.snapshot,
  computed_at = EXCLUDED.computed_at,
  updated_at  = now()
`
	_, err := pool.Exec(ctx, q, s.UserID, s.LocationKey, s.Snapshot, s.ComputedAt)
	return err
}
