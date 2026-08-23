-- Schema for the iOS client: mobile auth, push delivery, and the ledger
-- change push forces. See docs/ios-app-plan.md §4.1 and §4.2.

-- 1. App-initiated OAuth handshakes.
-- The mobile login is the same PKCE dance against Spotify, but it ends in a
-- one-time code the app redeems rather than a Set-Cookie the app cannot read.
-- app_challenge is S256(verifier) for a *second* PKCE-shaped exchange, this
-- one between the app and us — it is what makes a stolen code useless on its
-- own. NULL for every browser login, which is what tells the callback which
-- ending to serve.
ALTER TABLE oauth_handshakes
  ADD COLUMN IF NOT EXISTS app_challenge TEXT;

-- 2. The one-time codes themselves.
-- Deliberately not folded into oauth_handshakes: that row is consumed by
-- /callback (TakeHandshake deletes it), and this one is created there. The
-- lifecycles are disjoint and the TTLs differ by two orders of magnitude
-- (10 minutes vs 60 seconds).
--
-- session_id is the credential being escrowed. ON DELETE CASCADE means a
-- session revoked between mint and redeem takes its pending code with it,
-- rather than leaving a code that exchanges into a dead session.
CREATE TABLE IF NOT EXISTS mobile_auth_codes (
  code          TEXT PRIMARY KEY,
  session_id    TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  app_challenge TEXT NOT NULL,
  expires_at    TIMESTAMPTZ NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mobile_auth_codes_expires_at_idx ON mobile_auth_codes(expires_at);

-- 3. APNs device tokens.
-- ON DELETE CASCADE is load-bearing rather than tidy: DELETE /me/account has
-- to take device tokens with it, or a deleted user's phone keeps receiving
-- pushes with no account left to turn them off from.
--
-- environment is per-row, not per-deployment: a TestFlight build and a debug
-- build on the same account talk to different APNs hosts, and sending a
-- sandbox token to the production host is a BadDeviceToken, not a fallback.
CREATE TABLE IF NOT EXISTS user_devices (
  user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_token TEXT NOT NULL,
  environment  TEXT NOT NULL,          -- 'sandbox' | 'production'
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  disabled_at  TIMESTAMPTZ,            -- set on APNs 410 Gone / BadDeviceToken
  PRIMARY KEY (user_id, device_token)
);
-- The push worker's read is "live tokens for this user".
CREATE INDEX IF NOT EXISTS user_devices_live_idx
  ON user_devices(user_id) WHERE disabled_at IS NULL;

-- 4. Push preference.
-- A separate column rather than a reuse of instant_notify_opt_in. "Push me but
-- do not email me" is an ordinary preference, and overloading the existing
-- flag makes it unexpressible — the user would have to accept email to get
-- push. Defaults false: push requires an explicit grant on the device anyway,
-- so defaulting true would misreport intent for every existing user.
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS push_opt_in BOOLEAN NOT NULL DEFAULT false;

-- 5. The already-sent ledger gains a channel. THIS IS THE LOAD-BEARING PART.
--
-- user_digest_sent was PRIMARY KEY (user_id, dedup_key) with no channel, and
-- the daily digest and instant-notify share it *deliberately*: a show emailed
-- once must not be emailed again by the other path. Push cannot join that
-- arrangement unchanged. If SendPushWorker writes these rows, it suppresses
-- the email for that show; if it reads them, a user opted into both channels
-- gets exactly one of the two, decided by whichever worker happened to run
-- first. Neither failure raises an error or logs anything — the user just
-- silently stops getting one of the things they asked for.
--
-- Chosen: widen the key to (user_id, dedup_key, channel), rather than a
-- separate user_push_sent table.
--
-- Why this and not the separate table. The separate table touches less
-- existing code — nothing above needs a new argument — but it re-answers the
-- same question in two places, and every consumer then has to remember both.
-- The janitor would need a second prune step, "has this user heard about this
-- show at all" becomes a UNION, and a fourth channel later (a Web Push, an
-- SMS) means a third table on the same pattern. One column makes the channel
-- an ordinary dimension of one ledger: the janitor prunes one table, the
-- filter reads one table, and adding a channel adds a value, not a schema.
-- The cost is that db.FilterUnsentDedupKeys / RecordDigestSent / CountDigestSent
-- all grow a channel parameter and every caller must state which it means.
-- That cost is the point — it makes the choice explicit at each call site
-- rather than implicit in which table was imported.
--
-- Backfill is 'email': every existing row was written by the digest or
-- instant-notify path, which are the same channel and always have been.
ALTER TABLE user_digest_sent
  ADD COLUMN IF NOT EXISTS channel TEXT NOT NULL DEFAULT 'email';

ALTER TABLE user_digest_sent
  DROP CONSTRAINT IF EXISTS user_digest_sent_pkey;
ALTER TABLE user_digest_sent
  ADD PRIMARY KEY (user_id, dedup_key, channel);
