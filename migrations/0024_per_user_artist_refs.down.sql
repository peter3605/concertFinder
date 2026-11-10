-- Safe only while concerts.data still carries artist id/genres, i.e. before the
-- strip migration that follows this one. After it, the previous binary reads
-- artist identity from rows that no longer hold any.
BEGIN;
ALTER TABLE user_saved_concerts DROP COLUMN artist_ref;
ALTER TABLE user_concert_snapshots DROP COLUMN artist_refs;
COMMIT;
