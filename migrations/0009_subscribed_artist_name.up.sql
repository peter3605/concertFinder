-- Cache the artist display name at subscribe time so the "manage
-- subscriptions" UI can render the list without a batch call back to
-- Spotify's /artists API. Nullable to keep the migration safe over an
-- existing row set, but new inserts should always populate it.
ALTER TABLE user_subscribed_artists
  ADD COLUMN IF NOT EXISTS display_name TEXT;
