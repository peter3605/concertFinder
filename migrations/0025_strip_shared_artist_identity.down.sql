-- Nothing to restore, deliberately. The stripped IDs and genres were the last
-- Spotify writer's, not any particular viewer's, and every viewer's own copy
-- is still in user_concert_snapshots.artist_refs and
-- user_saved_concerts.artist_ref. Rescans do not repopulate them either: since
-- 0024 no writer puts them in the shared row.
--
-- Consequence for a rollback: the binary before 0024 reads artist identity
-- from concerts.data, so it must not be run against a database this migration
-- has touched. Roll back to a commit at or after 0024 instead.
SELECT 1;
