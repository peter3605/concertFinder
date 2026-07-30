-- Normalize concert data out of user_concert_snapshots.
-- Before: snapshot column was a JSONB blob containing full concert records
-- (artist name, venue, city, links, ...). Every user had their own copy
-- of every concert — the same Taylor Swift show serialized N times across
-- N users.
-- After: one row per unique dedup_key in `concerts`. Snapshots hold only
-- the array of dedup_keys the user cares about. Storage drops ~10-100x at
-- multi-user scale; reads become "SELECT ... WHERE dedup_key = ANY($1)".

CREATE TABLE IF NOT EXISTS concerts (
  dedup_key    TEXT PRIMARY KEY,
  data         JSONB NOT NULL,       -- serialized concerts.Concert
  event_date   TIMESTAMPTZ NOT NULL, -- pulled out for range queries + janitor pruning
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS concerts_event_date_idx ON concerts(event_date);
CREATE INDEX IF NOT EXISTS concerts_updated_at_idx ON concerts(updated_at);

-- Replace the JSONB snapshot column with an array of pointers. During the
-- migration we drop the old column entirely — the scan worker will
-- repopulate on next run, and any snapshot older than the last cron cycle
-- would have been recomputed regardless.
ALTER TABLE user_concert_snapshots DROP COLUMN IF EXISTS snapshot;
ALTER TABLE user_concert_snapshots
  ADD COLUMN IF NOT EXISTS dedup_keys TEXT[] NOT NULL DEFAULT '{}';
