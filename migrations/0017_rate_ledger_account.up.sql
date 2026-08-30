-- Account-wide, per-source, per-day request counter.
--
-- rate_ledger (0004) bounds what any ONE user can spend. Nothing bounded what
-- every user spends together, and the number that matters upstream is exactly
-- that: Ticketmaster's quota is 5000 req/day for the API key, not per user of
-- ours. With a 500/day per-user cap, ten users doing cold scans on the same
-- day exhaust the account, and the eleventh user's scan gets 403s that look
-- like an artist simply having no shows.
--
-- Kept as its own table rather than SUM(count) over rate_ledger so the check
-- is a single atomic upsert returning the new total -- the same shape as the
-- per-user charge -- instead of a full scan of the day's rows on every
-- reservation, and so it stays correct if a second replica ever exists.
--
-- No user_id and therefore no FK: this row outlives any individual account,
-- and deleting a user must not silently return quota that was really spent.
CREATE TABLE IF NOT EXISTS rate_ledger_account (
  source TEXT NOT NULL,
  day    DATE NOT NULL,
  count  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (source, day)
);
CREATE INDEX IF NOT EXISTS rate_ledger_account_day_idx ON rate_ledger_account(day);
