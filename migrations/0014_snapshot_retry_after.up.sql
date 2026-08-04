-- When a scan is cut short because the user's daily upstream quota is spent,
-- the condition cannot clear until the ledger's UTC day rolls over. But an
-- incomplete snapshot is treated as stale, so the SWR read path re-enqueued a
-- scan on every 10s poll, and river retried on top of that — a loop that
-- could not possibly succeed until midnight.
--
-- retry_after records the earliest moment another scan could plausibly do
-- better. The read path skips enqueueing until then; NULL means "no reason to
-- wait", which is the case for every other kind of incompleteness (a budget
-- overrun, for instance, is worth retrying immediately).
ALTER TABLE user_concert_snapshots
  ADD COLUMN IF NOT EXISTS retry_after TIMESTAMPTZ;
