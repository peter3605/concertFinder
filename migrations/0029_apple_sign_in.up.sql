-- Sign in with Apple (CF-35). docs/public-path.md section 4.1 is the design.
--
-- 1. The web flow's nonce rides in the handshake beside state. The raw value
--    stays here; Apple only ever sees its SHA-256 (apple.HashNonce), and the
--    identity token's nonce claim must equal that hash.
ALTER TABLE oauth_handshakes
  ADD COLUMN IF NOT EXISTS nonce TEXT;

-- 2. The Apple refresh token, kept for one reason: deleting the account must
--    call Apple's /auth/revoke with it. Encrypted with ENCRYPTION_KEY exactly
--    as users.encrypted_refresh_token is (AES-256-GCM, per-token nonce).
--
--    client_id is stored because a token can only be revoked by the client
--    that obtained it, and that differs by route: the Services ID for a web
--    sign-in, the bundle ID for the app. One row per user, holding the token
--    from the latest sign-in.
CREATE TABLE apple_tokens (
  user_id                 UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  client_id               TEXT NOT NULL,
  encrypted_refresh_token BYTEA NOT NULL,
  refresh_token_nonce     BYTEA NOT NULL,
  updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 3. The revocation retry queue. A row is written in the same transaction
--    that deletes the user, so the token cannot be lost between the two, and
--    it is deliberately NOT a foreign key to users: it has to outlive the
--    account. A RevokeAppleToken job, inserted in that same transaction,
--    deletes the row once Apple accepts the revoke (or says the token is
--    already gone). A row still here after the job gave up is the record
--    that a revoke never landed.
--
--    It names no user. After deletion there is nobody to name, and keeping
--    the user id would be retaining an identifier for a deleted account.
CREATE TABLE apple_revocations (
  id                      UUID PRIMARY KEY,
  client_id               TEXT NOT NULL,
  encrypted_refresh_token BYTEA NOT NULL,
  refresh_token_nonce     BYTEA NOT NULL,
  attempts                INT NOT NULL DEFAULT 0,
  last_error              TEXT,
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
