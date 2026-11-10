-- Per-user artist identity, moved off the shared concerts row (CF-28,
-- docs/public-path.md section 7).
--
-- concerts.data is shared by dedup_key, which is artist name + local day +
-- venue + city, and UpsertConcerts is last-writer-wins. Until now each scan
-- wrote the artist's Spotify ID and Spotify genres into that row, so two users
-- at one show read whichever of them scanned last. Once a user can reach the
-- feed without Spotify that is wrong in both directions: a manual user is
-- served Spotify-derived fields, and a manual scan overwrites a Spotify user's
-- artist ID, pointing their subscribe bell at an artist that is not theirs.
--
-- The artist a user sees for a row now comes from their own reference:
--   user_concert_snapshots.artist_refs  dedup_key -> {"id","name","genres"}
--   user_saved_concerts.artist_ref      {"id","name","genres"}, copied at save
--                                       time, because a save outlives the feed
--
-- The backfill reads concerts.data as it stands. Every row in it was written by
-- a Spotify user's scan, so its artist is correct for every viewer who has it
-- today.
--
-- This migration does NOT strip id/genres from concerts.data. The readers that
-- stop trusting those fields have to be deployed, and stay deployed, before the
-- fields go: a rollback onto stripped rows would leave the previous binary with
-- no artist IDs at all, and every subscribe control silently inert. The strip is
-- a later migration, shipped on its own.
ALTER TABLE user_concert_snapshots
  ADD COLUMN artist_refs JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE user_saved_concerts
  ADD COLUMN artist_ref JSONB;

UPDATE user_concert_snapshots s
SET artist_refs = COALESCE((
  SELECT jsonb_object_agg(c.dedup_key, c.data -> 'artist')
  FROM concerts c
  WHERE c.dedup_key = ANY (s.dedup_keys)
    AND c.data ? 'artist'
), '{}'::jsonb);

UPDATE user_saved_concerts s
SET artist_ref = c.data -> 'artist'
FROM concerts c
WHERE c.dedup_key = s.dedup_key
  AND c.data ? 'artist';
