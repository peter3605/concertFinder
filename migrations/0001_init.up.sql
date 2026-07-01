CREATE TABLE IF NOT EXISTS users (
  id                       UUID PRIMARY KEY,
  spotify_user_id          TEXT NOT NULL UNIQUE,
  display_name             TEXT,
  encrypted_refresh_token  BYTEA NOT NULL,
  refresh_token_nonce      BYTEA NOT NULL,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
  id           TEXT PRIMARY KEY,
  user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS user_locations (
  user_id        UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  latitude       DOUBLE PRECISION NOT NULL,
  longitude      DOUBLE PRECISION NOT NULL,
  radius_miles   INTEGER NOT NULL DEFAULT 50,
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS affinity_profiles (
  user_id      UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  artists      JSONB NOT NULL,
  computed_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS artist_resolutions (
  spotify_artist_id           TEXT PRIMARY KEY,
  ticketmaster_attraction_id  TEXT,
  bandsintown_name            TEXT,
  official_url                TEXT,
  resolved_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS concert_cache (
  cache_key   TEXT PRIMARY KEY,
  results     JSONB NOT NULL,
  fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS concert_cache_fetched_at_idx ON concert_cache(fetched_at);
