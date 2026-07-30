-- Per-user, per-concert record of what we've already sent in a digest.
-- Enables an exact "net new since last digest" query and gives us
-- idempotency: a retried SendDigest job filters out already-sent items.
CREATE TABLE IF NOT EXISTS user_digest_sent (
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  dedup_key  TEXT NOT NULL,
  sent_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, dedup_key)
);
CREATE INDEX IF NOT EXISTS user_digest_sent_sent_at_idx ON user_digest_sent(sent_at);
