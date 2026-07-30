-- Persistent MusicBrainz URL cache: survives restart and shared across
-- replicas. Empty string = MB tried but found nothing (negative cache).
CREATE TABLE IF NOT EXISTS mb_url_cache (
  artist_key   TEXT PRIMARY KEY,        -- lowercased/trimmed artist name
  official_url TEXT NOT NULL DEFAULT '',
  resolved_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Persistent venue geocode cache: (city, state, country) → lat/lng. Cities
-- don't move so this can be reused indefinitely. ok=false = tried and no
-- match (negative cache).
CREATE TABLE IF NOT EXISTS venue_geo_cache (
  place_key   TEXT PRIMARY KEY,          -- lowercase(city|state|country)
  latitude    DOUBLE PRECISION NOT NULL DEFAULT 0,
  longitude   DOUBLE PRECISION NOT NULL DEFAULT 0,
  ok          BOOLEAN NOT NULL,
  resolved_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- DB-backed handshake store for OAuth flows. Enables multi-instance
-- deploys — /login on replica A, /callback on B still works. Small enough
-- that a periodic prune keeps the table tiny.
CREATE TABLE IF NOT EXISTS oauth_handshakes (
  handshake_key TEXT PRIMARY KEY,
  verifier      TEXT NOT NULL,
  state         TEXT NOT NULL,
  expires_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS oauth_handshakes_expires_at_idx ON oauth_handshakes(expires_at);
