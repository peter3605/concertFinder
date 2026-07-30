-- Per-user, per-source, per-day request counter for enforcing shared upstream
-- quotas (design §8.3). Ticketmaster's 5000 req/day account-wide cap can
-- otherwise be exhausted by one heavy user. Row is upserted on every fresh
-- (cache-miss) upstream call.
CREATE TABLE IF NOT EXISTS rate_ledger (
  user_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source   TEXT NOT NULL,
  day      DATE NOT NULL,
  count    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, source, day)
);
CREATE INDEX IF NOT EXISTS rate_ledger_day_idx ON rate_ledger(day);
