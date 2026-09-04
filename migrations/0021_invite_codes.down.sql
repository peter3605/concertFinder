ALTER TABLE oauth_handshakes DROP COLUMN IF EXISTS invite_code;
-- Drop the column before the table: users.invited_with references it, and the
-- FK would otherwise refuse the DROP.
ALTER TABLE users DROP COLUMN IF EXISTS invited_with;
DROP TABLE IF EXISTS invite_codes;
