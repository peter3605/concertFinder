DROP TABLE IF EXISTS apple_revocations;
DROP TABLE IF EXISTS apple_tokens;
ALTER TABLE oauth_handshakes DROP COLUMN IF EXISTS nonce;
