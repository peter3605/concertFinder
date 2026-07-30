ALTER TABLE users
  DROP COLUMN IF EXISTS digest_last_sent_at,
  DROP COLUMN IF EXISTS digest_opt_in,
  DROP COLUMN IF EXISTS email;
