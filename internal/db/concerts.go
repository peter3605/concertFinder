// Package db access for the normalized concerts table (0012). Each concert
// (identified by dedup_key) is stored once in `concerts`; user_concert_snapshots
// holds arrays of dedup_keys pointing at these rows.
package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UpsertConcerts stores or refreshes a batch of concerts. Callers pass raw
// JSON-serialized bodies alongside their event dates so this layer stays
// concert-shape-agnostic.
type ConcertRow struct {
	DedupKey  string
	Data      []byte    // serialized concerts.Concert
	EventDate time.Time // pulled out for indexed range queries
}

// UpsertConcerts inserts / updates a batch of concert rows in one round trip.
// Uses UNNEST for efficiency vs N individual INSERTs.
func UpsertConcerts(ctx context.Context, pool *pgxpool.Pool, rows []ConcertRow) error {
	if len(rows) == 0 {
		return nil
	}
	keys := make([]string, len(rows))
	datas := make([][]byte, len(rows))
	dates := make([]time.Time, len(rows))
	for i, r := range rows {
		keys[i] = r.DedupKey
		datas[i] = r.Data
		dates[i] = r.EventDate
	}
	const q = `
INSERT INTO concerts (dedup_key, data, event_date, updated_at)
SELECT k, d::jsonb, dt, now()
FROM unnest($1::text[], $2::jsonb[], $3::timestamptz[]) AS t(k, d, dt)
ON CONFLICT (dedup_key) DO UPDATE SET
  data       = EXCLUDED.data,
  event_date = EXCLUDED.event_date,
  updated_at = now()
`
	_, err := pool.Exec(ctx, q, keys, datas, dates)
	return err
}

// GetConcertsByDedupKeys returns the raw JSON bodies for the given keys,
// in the order Postgres returns them (which is not the input order — the
// caller is responsible for reassembling display order if needed).
func GetConcertsByDedupKeys(ctx context.Context, pool *pgxpool.Pool, keys []string) ([][]byte, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	const q = `SELECT data FROM concerts WHERE dedup_key = ANY($1::text[])`
	rows, err := pool.Query(ctx, q, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([][]byte, 0, len(keys))
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
