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
	// ArtistRefs is this user's artist identity for each dedup_key, as a JSON
	// object of dedup_key -> {"id","name","genres"} (migration 0024). It lives
	// here, not in the shared `concerts` row, because that row is shared by
	// every user whose scan produced the key and is last-writer-wins; see
	// concerts.ApplyArtistRefs. Raw JSON so this package stays
	// concert-shape-agnostic. Nil is stored as an empty object.
	ArtistRefs []byte
	// GenreHorizon is the venue-local day ("2006-01-02") before which the
	// genre part of this feed is complete (migration 0027). Genre listings
	// are capped date-sorted prefixes, so the feed holds the next shows up
	// to here and none after. "" when the user has no genre picks or nothing
	// was cut.
	GenreHorizon string
}

// GetConcertSnapshot returns (snapshot, true, nil) on hit, (zero, false, nil) on miss.
func GetConcertSnapshot(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, locationKey string) (ConcertSnapshot, bool, error) {
	const q = `
SELECT user_id, location_key, dedup_keys, computed_at, complete, retry_after, artist_refs, COALESCE(genre_horizon, '')
FROM user_concert_snapshots
WHERE user_id = $1 AND location_key = $2
`
	var s ConcertSnapshot
	err := pool.QueryRow(ctx, q, userID, locationKey).Scan(&s.UserID, &s.LocationKey, &s.DedupKeys, &s.ComputedAt, &s.Complete, &s.RetryAfter, &s.ArtistRefs, &s.GenreHorizon)
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
INSERT INTO user_concert_snapshots (user_id, location_key, dedup_keys, computed_at, complete, retry_after, artist_refs, genre_horizon, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7::jsonb, '{}'::jsonb), NULLIF($8::text, ''), now())
ON CONFLICT (user_id, location_key) DO UPDATE SET
  dedup_keys  = EXCLUDED.dedup_keys,
  computed_at = EXCLUDED.computed_at,
  complete    = EXCLUDED.complete,
  retry_after = EXCLUDED.retry_after,
  artist_refs = EXCLUDED.artist_refs,
  genre_horizon = EXCLUDED.genre_horizon,
  updated_at  = now()
`
	_, err := pool.Exec(ctx, q, s.UserID, s.LocationKey, s.DedupKeys, s.ComputedAt, s.Complete, s.RetryAfter, nullableJSON(s.ArtistRefs), s.GenreHorizon)
	return err
}

// nullableJSON passes an empty body as SQL NULL, so the COALESCE above turns
// it into an empty object rather than Postgres rejecting an empty string as
// invalid json.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// GetArtistRefs returns this user's artist identity for each of keys that
// they hold one for, as raw JSON objects keyed by dedup_key. It is the read
// for paths that start from dedup_keys rather than from a snapshot -- the
// instant-notify and push jobs.
//
// A key may sit in several of the user's snapshots (one per location) and in
// their saves. The newest snapshot wins, then the save: the snapshot reflects
// the current profile, while a save's ref is a copy taken when it was made.
// Keys the user holds no ref for are absent, which concerts.ApplyArtistRefs
// treats as "no artist ID", never as "use the shared row's".
func GetArtistRefs(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, keys []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(keys) == 0 {
		return out, nil
	}
	const q = `
SELECT DISTINCT ON (k) k, ref
FROM (
  SELECT k, s.artist_refs -> k AS ref, 0 AS pri, s.computed_at AS at
  FROM user_concert_snapshots s
  CROSS JOIN unnest($2::text[]) AS k
  WHERE s.user_id = $1 AND s.artist_refs ? k
  UNION ALL
  SELECT sc.dedup_key, sc.artist_ref, 1, sc.saved_at
  FROM user_saved_concerts sc
  WHERE sc.user_id = $1 AND sc.dedup_key = ANY ($2::text[]) AND sc.artist_ref IS NOT NULL
) x
ORDER BY k, pri, at DESC
`
	rows, err := pool.Query(ctx, q, userID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k   string
			ref []byte
		)
		if err := rows.Scan(&k, &ref); err != nil {
			return nil, err
		}
		out[k] = ref
	}
	return out, rows.Err()
}
