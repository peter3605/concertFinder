-- Index user_concert_snapshots.updated_at.
--
-- PruneStaleSnapshots (internal/db/janitor.go) filters on updated_at, but the
-- only index this table has ever had is on computed_at (0002). The nightly
-- janitor therefore sequentially scans it, on a database pinned at 0.25 CU
-- whose free-plan budget is a compute-hour allowance the app already spends by
-- never scaling to zero.
--
-- Plain CREATE INDEX, not CONCURRENTLY: the migrator runs each file inside a
-- transaction (internal/db/migrate.go), and CREATE INDEX CONCURRENTLY cannot
-- run in one. This table holds a row per (user, location) — small enough that
-- the brief write lock is cheaper than the machinery a concurrent build would
-- need.
CREATE INDEX IF NOT EXISTS user_concert_snapshots_updated_at_idx
  ON user_concert_snapshots(updated_at);
