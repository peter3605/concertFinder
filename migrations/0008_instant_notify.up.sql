-- Instant email on new concert discovery, gated on a per-user subscribed
-- artist list. Separate opt-in from digest_opt_in so a user can pick daily
-- digest, instant, both, or neither. Reuses user_digest_sent so the daily
-- digest doesn't re-notify about concerts already sent instantly.
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS instant_notify_opt_in BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS user_subscribed_artists (
  user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  spotify_artist_id  TEXT NOT NULL,
  subscribed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, spotify_artist_id)
);
CREATE INDEX IF NOT EXISTS user_subscribed_artists_user_idx
  ON user_subscribed_artists(user_id);
