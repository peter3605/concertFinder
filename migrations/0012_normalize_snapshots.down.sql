ALTER TABLE user_concert_snapshots DROP COLUMN IF EXISTS dedup_keys;
ALTER TABLE user_concert_snapshots ADD COLUMN IF NOT EXISTS snapshot JSONB NOT NULL DEFAULT '[]';
DROP TABLE IF EXISTS concerts;
