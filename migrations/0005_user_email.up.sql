-- Email + digest opt-in for the Phase 3 daily-digest feature (design §10.3).
-- Email is only populated once the user re-authenticates with the
-- user-read-email scope; nulls are expected for legacy users. digest_opt_in
-- defaults to false so we never send email without an explicit opt-in.
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS email                TEXT,
  ADD COLUMN IF NOT EXISTS digest_opt_in        BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS digest_last_sent_at  TIMESTAMPTZ;
