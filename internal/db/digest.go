package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FilterUnsentDedupKeys returns the subset of candidates that this user has
// NOT already received in a digest. Used by the digest worker for exact
// "net new since last email" semantics (design §10.3).
func FilterUnsentDedupKeys(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, candidates []string) (map[string]struct{}, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	const q = `SELECT dedup_key FROM user_digest_sent WHERE user_id = $1 AND dedup_key = ANY($2)`
	rows, err := pool.Query(ctx, q, userID, candidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sent := map[string]struct{}{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		sent[k] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	unsent := map[string]struct{}{}
	for _, k := range candidates {
		if _, alreadySent := sent[k]; !alreadySent {
			unsent[k] = struct{}{}
		}
	}
	return unsent, nil
}

// RecordDigestSent atomically inserts sent-record rows for every provided
// dedup_key. Idempotent: existing rows are left alone.
func RecordDigestSent(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, dedupKeys []string) error {
	if len(dedupKeys) == 0 {
		return nil
	}
	const q = `
INSERT INTO user_digest_sent (user_id, dedup_key)
SELECT $1, k FROM unnest($2::text[]) AS k
ON CONFLICT (user_id, dedup_key) DO NOTHING
`
	_, err := pool.Exec(ctx, q, userID, dedupKeys)
	return err
}
