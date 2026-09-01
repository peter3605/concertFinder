-- Stop storing the live session credential.
--
-- sessions.id held the 32-byte random token verbatim, and that token is both
-- the cf_session cookie and the iOS Authorization: Bearer value. So every
-- nightly pg_dump in S3 (scripts/backup-db.sh) was a file of working
-- credentials for every signed-in user, as was any hot standby, any psql
-- session, and any log line that ever echoed a row.
--
-- id stays the primary key -- mobile_auth_codes.session_id references it with
-- ON DELETE CASCADE, and that cascade is what stops a revoked session leaving
-- a redeemable one-time code behind -- but it now holds a fresh opaque UUID
-- with no relationship to the token. Authentication goes by token_hash,
-- sha256(token) in lowercase hex. An unsalted SHA-256 is the right primitive
-- here and not a shortcut: the input is 32 bytes of crypto/rand, so there is
-- no dictionary to precompute and nothing for a work factor to buy.
--
-- Nullable, and deliberately not backfilled. There is no way to derive the
-- hash of a token we no longer have, so every session that predates this
-- migration simply stops resolving and its owner signs in again. The
-- alternative -- keeping the old rows matchable -- is keeping the credential.
--
-- NULL also has a second, load-bearing use: it is the escrow state for the
-- mobile login. /api/auth/callback creates the app's session row with no
-- token_hash, which makes it unauthenticatable, and the exchange endpoint
-- claims it by writing the hash of a token it mints and returns exactly once
-- (db.ClaimSessionToken). That is why the token never has to be parked in
-- mobile_auth_codes. Postgres allows many NULLs under a unique index, so
-- unredeemed escrows and legacy rows coexist; `WHERE token_hash = $1` can
-- never match either, since NULL is not equal to anything.
ALTER TABLE sessions
  ADD COLUMN IF NOT EXISTS token_hash TEXT;

-- Unique because a collision would be two users sharing one credential, and
-- an index because this is now the lookup on every authenticated request.
CREATE UNIQUE INDEX IF NOT EXISTS sessions_token_hash_idx ON sessions(token_hash);
