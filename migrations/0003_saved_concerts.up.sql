-- User-saved concerts (heart/bookmark). Keyed on dedup_key so a save
-- survives snapshot refreshes and re-scans as long as the concert still
-- matches on (artist, date, venue, city). Reads join against the current
-- user_concert_snapshots row, so orphan saves (concert no longer in the
-- snapshot) are invisible to the user without any proactive cleanup.
CREATE TABLE IF NOT EXISTS user_saved_concerts (
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  dedup_key  TEXT NOT NULL,
  saved_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, dedup_key)
);
CREATE INDEX IF NOT EXISTS user_saved_concerts_user_idx
  ON user_saved_concerts(user_id);
