-- Staged admission: a signup needs an invite code.
--
-- Until now the only thing standing between the open internet and a Spotify
-- OAuth grant was Spotify's own Development Mode allowlist, maintained by hand
-- in their dashboard. That is a real gate, but it is not ours and it
-- disappears the moment Extended Quota Mode is granted -- at which point the
-- deployment goes from "25 accounts the operator typed in" to "anyone with a
-- Spotify account", with nothing in this repo noticing. This table is the
-- replacement gate, and it exists before that grant rather than after.
--
-- What it is bounding is Ticketmaster, not Spotify. RATE_CAP_TM_ACCOUNT_DAILY
-- is 5000/day for the whole deployment, and jobs.scanQuotaFor reserves
-- artists*2 permits up front -- 400 for a full spotify.MaxScoredArtists
-- profile. That is ~12 cold scans a day. The nightly fanout runs one scan per
-- user with a session in the last 14 days, so roughly twelve active users in
-- distinct cities is where the fanout alone exhausts the account ceiling.
-- concert_cache (12h) makes the second user in an already-scanned city far
-- cheaper, which is why the number is a floor rather than a hard limit -- but
-- it is a floor reached by succeeding, so the dial has to exist first.
--
-- Codes are stored in the clear, which is a deliberate departure from how
-- session tokens are handled (migration 0018) and worth saying why. A session
-- token authenticates as a user; an invite code does not authenticate as
-- anybody. Redeeming one still requires a complete Spotify OAuth grant from
-- the person redeeming it, so the blast radius of a code read out of a backup
-- is one signup slot, not one account. Against that, the operator has to be
-- able to answer "which code did I give to whom" and re-send one that got
-- lost in a spam folder, which a hash makes impossible. `note` exists for the
-- first half of that question.
CREATE TABLE IF NOT EXISTS invite_codes (
  code            TEXT PRIMARY KEY,
  -- Free text for the operator: who this was minted for. Never shown to the
  -- person redeeming it.
  note            TEXT NOT NULL DEFAULT '',
  -- A code can seat more than one person (a household, a group of testers).
  -- 1 is the ordinary case.
  max_redemptions INT NOT NULL DEFAULT 1 CHECK (max_redemptions > 0),
  redemptions     INT NOT NULL DEFAULT 0 CHECK (redemptions >= 0),
  -- NULL means it never expires. An expiry is the difference between handing
  -- out ten codes and handing out ten codes that stop mattering.
  expires_at      TIMESTAMPTZ,
  -- Revocation without deletion: a redeemed code has to stay readable to
  -- explain where a user came from, so "off" is a timestamp rather than a
  -- DELETE.
  disabled_at     TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Which code admitted this user. NULL for everyone who predates this
-- migration, which is exactly the grandfathering rule the callback applies:
-- admission is checked only when a login would CREATE a user, so existing
-- accounts keep working without anybody backfilling anything.
--
-- ON DELETE SET NULL rather than RESTRICT or CASCADE: retiring a spent code
-- must never be able to delete a user, and it must not be blocked by one
-- either. Losing the provenance is the acceptable half of that trade.
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS invited_with TEXT
    REFERENCES invite_codes(code) ON DELETE SET NULL;

-- The operator's list view sorts newest first; small table, but this is the
-- only query that isn't a primary-key lookup.
CREATE INDEX IF NOT EXISTS invite_codes_created_at_idx ON invite_codes(created_at DESC);

-- The code has to survive the round trip to Spotify's consent screen, so it
-- rides in the handshake row that already carries the PKCE verifier -- the
-- same place and for the same reason as app_challenge in migration 0016.
--
-- It is carried rather than redeemed at /login because most abandoned logins
-- are abandoned at Spotify's screen, which is after /login and before
-- /callback. Spending the code on the way out would let a curious click burn
-- somebody's invite.
ALTER TABLE oauth_handshakes
  ADD COLUMN IF NOT EXISTS invite_code TEXT;
