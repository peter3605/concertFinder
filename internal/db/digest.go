package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel names a delivery path in the already-sent ledger. Migration 0016
// widened user_digest_sent's primary key to include it; see that file for why
// a shared channel-less ledger silently suppressed one of two notifications
// for anyone opted into both.
//
// The daily digest and instant-notify both use ChannelEmail on purpose. They
// are two triggers for one channel, and a show emailed by either must not be
// emailed again by the other — that shared suppression is the existing,
// intended behaviour and predates this type.
type Channel string

const (
	ChannelEmail Channel = "email"
	ChannelPush  Channel = "push"
)

// CountDigestSent returns how many concerts this user has ever been
// sent on the given channel. Used to detect a first-ever digest exactly,
// rather than inferring it from "every candidate happens to be unsent" —
// which also fires for an established user who just moved cities.
func CountDigestSent(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, ch Channel) (int, error) {
	const q = `SELECT count(*) FROM user_digest_sent WHERE user_id = $1 AND channel = $2`
	var n int
	err := pool.QueryRow(ctx, q, userID, string(ch)).Scan(&n)
	return n, err
}

// FilterUnsentDedupKeys returns the subset of candidates that this user has
// NOT already received on the given channel. Used by the digest worker for
// exact "net new since last email" semantics (design §10.3), and by the push
// worker for the same semantics on its own channel.
//
// The channel argument is what keeps the two independent: filtering without
// it would let whichever worker ran first consume the other's candidates.
func FilterUnsentDedupKeys(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, ch Channel, candidates []string) (map[string]struct{}, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	const q = `SELECT dedup_key FROM user_digest_sent WHERE user_id = $1 AND channel = $2 AND dedup_key = ANY($3)`
	rows, err := pool.Query(ctx, q, userID, string(ch), candidates)
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
// dedup_key on the given channel. Idempotent: existing rows are left alone.
func RecordDigestSent(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, ch Channel, dedupKeys []string) error {
	if len(dedupKeys) == 0 {
		return nil
	}
	const q = `
INSERT INTO user_digest_sent (user_id, dedup_key, channel)
SELECT $1, k, $2 FROM unnest($3::text[]) AS k
ON CONFLICT (user_id, dedup_key, channel) DO NOTHING
`
	_, err := pool.Exec(ctx, q, userID, string(ch), dedupKeys)
	return err
}
