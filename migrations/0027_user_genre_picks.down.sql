ALTER TABLE user_concert_snapshots DROP COLUMN IF EXISTS genre_horizon;
ALTER TABLE concert_cache DROP COLUMN IF EXISTS horizon;
DROP TABLE IF EXISTS user_genre_picks;
