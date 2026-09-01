-- Bound how many distinct locations one account can open per day.
--
-- A scan is keyed by (user, location_key) and river's uniqueness only
-- collapses jobs that share that key, so every new location is a fresh
-- five-minute job with a full quota reservation. Five worker slots serve the
-- whole deployment, which means one account walking coordinates can occupy all
-- of them and starve every other user's scans, digests and pushes -- with no
-- error anywhere, because each individual job is perfectly legitimate.
--
-- One row per (user, day, location_key) rather than a counter, and that is the
-- whole point of a new table instead of a second use of rate_ledger.
-- rate_ledger counts calls; it cannot tell a location apart from one already
-- charged for today, so toggling between home and work would spend the
-- allowance twice a day and a commuter would be locked out by lunchtime. Set
-- membership is the thing being bounded, so a set is what gets stored:
-- re-entering a location already visited today is an ON CONFLICT DO NOTHING
-- and costs nothing.
--
-- The day is UTC to match rate_ledger and the scan quota that rolls over with
-- it -- one boundary for the whole system rather than one per feature.
CREATE TABLE IF NOT EXISTS user_location_visits (
  user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  day          DATE NOT NULL,
  location_key TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, day, location_key)
);

-- The janitor's prune is "everything older than N days", so it wants the day
-- alone; the cap check is covered by the primary key's leading columns.
CREATE INDEX IF NOT EXISTS user_location_visits_day_idx ON user_location_visits(day);
