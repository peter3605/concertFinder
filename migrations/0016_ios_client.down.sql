-- Reverse of 0016. Order matters: the ledger key is restored before the
-- channel column it references can be dropped.

-- The old primary key cannot be restored while rows differ only by channel,
-- so push rows are dropped first. That is the honest reversal: under the old
-- key those rows were never expressible.
DELETE FROM user_digest_sent WHERE channel <> 'email';

ALTER TABLE user_digest_sent
  DROP CONSTRAINT IF EXISTS user_digest_sent_pkey;
ALTER TABLE user_digest_sent
  ADD PRIMARY KEY (user_id, dedup_key);
ALTER TABLE user_digest_sent
  DROP COLUMN IF EXISTS channel;

ALTER TABLE users
  DROP COLUMN IF EXISTS push_opt_in;

DROP INDEX IF EXISTS user_devices_live_idx;
DROP TABLE IF EXISTS user_devices;

DROP INDEX IF EXISTS mobile_auth_codes_expires_at_idx;
DROP TABLE IF EXISTS mobile_auth_codes;

ALTER TABLE oauth_handshakes
  DROP COLUMN IF EXISTS app_challenge;
