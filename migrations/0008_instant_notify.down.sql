DROP TABLE IF EXISTS user_subscribed_artists;
ALTER TABLE users DROP COLUMN IF EXISTS instant_notify_opt_in;
