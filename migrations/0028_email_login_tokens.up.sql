-- Email sign-in links and address verification (CF-33). docs/public-path.md
-- section 4.2 is the design.
--
-- One row per mail sent. purpose 'login' signs somebody in (creating the
-- account if the address has no email identity yet and the invite gate lets
-- it); purpose 'verify' is a signed-in user confirming an address, which sets
-- users.email and adds an email identity to their account.
--
-- Neither the link token nor the 6-digit code is stored, only SHA-256 hashes,
-- the rule migration 0018 set for sessions: a pg_dump of this table must not
-- be a file of working sign-in links. code_hash is salted with token_hash so
-- two rows with the same code do not share a hash.
--
-- Rows are kept past consumption and expiry for two days so the per-address
-- and daily limits can count what was sent; the janitor removes them after.
CREATE TABLE email_login_tokens (
  token_hash    TEXT PRIMARY KEY,
  code_hash     TEXT NOT NULL,
  -- Lowercased, the same spelling user_identities.subject holds.
  email         TEXT NOT NULL,
  purpose       TEXT NOT NULL CHECK (purpose IN ('login', 'verify')),
  -- Set for 'verify' (the account the address is being added to) and never
  -- for 'login', where the account is resolved at consumption: resolving it
  -- at send time would go stale if the address were linked in between.
  user_id       UUID REFERENCES users(id) ON DELETE CASCADE,
  -- The code an unknown address signs up with. Redeemed only at consumption,
  -- in the transaction that creates the user, so an unclicked link spends
  -- nothing.
  invite_code   TEXT,
  -- The app's half of the mobile exchange, as on /api/auth/login?client=ios.
  app_challenge TEXT,
  attempts      INT NOT NULL DEFAULT 0,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at    TIMESTAMPTZ NOT NULL,
  consumed_at   TIMESTAMPTZ,
  CHECK ((purpose = 'verify') = (user_id IS NOT NULL))
);

CREATE INDEX email_login_tokens_email_created ON email_login_tokens (email, created_at);
CREATE INDEX email_login_tokens_created ON email_login_tokens (created_at);
