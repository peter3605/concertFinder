package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConcertSnapshot is the last completed concert scan for a (user, location).
// Post-normalization (migration 0012), the snapshot itself is just the
// ordered list of dedup_keys pointing into the shared `concerts` table;
// the SWR handler joins on read.
type ConcertSnapshot struct {
	UserID      uuid.UUID
	LocationKey string
	DedupKeys   []string
	ComputedAt  time.Time
	// Complete is false when the scan that produced this snapshot didn't
	// cover every artist — the budget expired, or an upstream source ran
	// out of the user's daily quota. The results are still served; the
	// flag stops the SWR handler from trusting them for a full staleness
	// window, since "few results" and "few results because we gave up
	// halfway" are otherwise indistinguishable.
	Complete bool
	// RetryAfter is set when the scan was cut short by something that time
	// alone fixes — currently only per-user daily quota exhaustion, which
	// clears when the rate ledger's UTC day rolls over. The read path
	// refuses to enqueue another scan before this instant; re-running
	// earlier is guaranteed to hit the same wall. Nil means "retry freely".
	RetryAfter *time.Time
}

// GetConcertSnapshot returns (snapshot, true, nil) on hit, (zero, false, nil) on miss.
func GetConcertSnapshot(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, locationKey string) (ConcertSnapshot, bool, error) {
	const q = `
SELECT user_id, location_key, dedup_keys, computed_at, complete, retry_after
FROM user_concert_snapshots
WHERE user_id = $1 AND location_key = $2
`
	var s ConcertSnapshot
	err := pool.QueryRow(ctx, q, userID, locationKey).Scan(&s.UserID, &s.LocationKey, &s.DedupKeys, &s.ComputedAt, &s.Complete, &s.RetryAfter)
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
INSERT INTO user_concert_snapshots (user_id, location_key, dedup_keys, computed_at, complete, retry_after, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (user_id, location_key) DO UPDATE SET
  dedup_keys  = EXCLUDED.dedup_keys,
  computed_at = EXCLUDED.computed_at,
  complete    = EXCLUDED.complete,
  retry_after = EXCLUDED.retry_after,
  updated_at  = now()
`
	_, err := pool.Exec(ctx, q, s.UserID, s.LocationKey, s.DedupKeys, s.ComputedAt, s.Complete, s.RetryAfter)
	return err
}
