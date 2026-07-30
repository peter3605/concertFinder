-- Snapshot of the last completed concert scan for a (user, location) pair.
-- Serves as the source of truth for GET /api/me/concerts under the SWR
-- pattern: the handler returns snapshot bytes directly and enqueues a
-- background refresh job when computed_at drifts past the staleness window.
CREATE TABLE IF NOT EXISTS user_concert_snapshots (
  user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  location_key  TEXT NOT NULL,                              -- 'lat,lng,radius' matching concert_cache convention
  snapshot      JSONB NOT NULL,                             -- serialized []concerts.Concert
  computed_at   TIMESTAMPTZ NOT NULL,                       -- when the scan finished
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, location_key)
);
CREATE INDEX IF NOT EXISTS user_concert_snapshots_computed_at_idx
  ON user_concert_snapshots(computed_at);
