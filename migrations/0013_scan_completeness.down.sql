DROP INDEX IF EXISTS sessions_last_seen_at_idx;
ALTER TABLE user_concert_snapshots DROP COLUMN IF EXISTS complete;
