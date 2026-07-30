-- Revert only re-adds the column. The historical timestamps are gone.
ALTER TABLE users ADD COLUMN IF NOT EXISTS digest_last_sent_at TIMESTAMPTZ;
