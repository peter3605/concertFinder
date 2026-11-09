-- A login identity stops being a column on users and becomes a row of its own,
-- so an account can exist without Spotify and can hold more than one way in.
-- docs/public-path.md section 3 is the design.
--
-- Sessions reference the opaque users.id, so nothing about them changes and
-- every token_hash in the table keeps authenticating across this migration.
--
-- spotify_user_id stays on users, and stays UNIQUE, because -grant-admin and
-- UpsertUserWithAdmission still address accounts by it. A UNIQUE column admits
-- any number of NULLs, so accounts without Spotify do not collide.
--
-- The column going nullable is what makes this migration dangerous to the Go
-- code rather than to the data: scanning NULL into a string is an error, so
-- every SELECT or RETURNING of it needs COALESCE(spotify_user_id, ''). NULL
-- rather than '' is still the right stored value, because `WHERE
-- spotify_user_id = $1` can never match NULL, which keeps an empty argument
-- from resolving to some manual user.
CREATE TABLE user_identities (
  provider      TEXT NOT NULL CHECK (provider IN ('spotify', 'apple', 'email')),
  -- Spotify ID, Apple sub, or lowercased email address.
  subject       TEXT NOT NULL,
  user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_login_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, subject),
  -- Also the index the cascade and the session join's auth_methods read use.
  UNIQUE (user_id, provider)
);

INSERT INTO user_identities (provider, subject, user_id, created_at)
SELECT 'spotify', spotify_user_id, id, created_at FROM users;

ALTER TABLE users ALTER COLUMN spotify_user_id DROP NOT NULL;
