-- Two fixes from the codebase audit.

-- 1. Snapshot completeness.
-- A scan that ran out of its 5-minute budget, or whose upstream quota was
-- exhausted partway through, used to write a snapshot indistinguishable
-- from a complete one — stamping computed_at = now() and thereby telling
-- the SWR handler not to refresh for SNAPSHOT_STALE_AFTER_HOURS. Partial
-- results are still worth serving (better than an empty page), so we keep
-- writing them and record the fact instead; the handler treats
-- complete = false as stale regardless of age.
-- Existing rows default to true: they were written by the old code path,
-- which only ever produced rows it believed were complete.
ALTER TABLE user_concert_snapshots
  ADD COLUMN IF NOT EXISTS complete BOOLEAN NOT NULL DEFAULT true;

-- 2. Index sessions.last_seen_at.
-- Both nightly fanout workers filter on it:
--   SELECT DISTINCT user_id FROM sessions WHERE last_seen_at > now() - '14 days'
-- and there was no index, so each was a sequential scan over a table that
-- (until the janitor step added alongside this migration) never shed a row.
CREATE INDEX IF NOT EXISTS sessions_last_seen_at_idx ON sessions(last_seen_at);
