-- Strip artist identity from concerts rows written before migration 0024
-- (CF-28, docs/public-path.md section 7).
--
-- Since 0024 no scan writes artist.id or artist.genres into concerts.data:
-- the row is shared by every user whose scan produced its dedup_key and is
-- last-writer-wins, so each user's identity lives on their own snapshot
-- (artist_refs) and saves (artist_ref) instead, and every reader overlays it
-- with concerts.ApplyArtistRefs. Rows written before that still carry the last
-- Spotify writer's ID and genres. Nothing reads them any more; this removes
-- them so the table holds no viewer's identity at all, which is the state the
-- rest of the public path is built on.
--
-- ORDERING, which is the whole risk: this must run only on a deployment whose
-- readers no longer trust these fields, i.e. after 0024's binary is live, and
-- that binary must never be rolled back past afterwards. The binary before
-- 0024 reads artist identity from exactly these fields, so on stripped rows it
-- would render every act with no artist ID: every subscribe bell off, every
-- instant alert unmatched, every genre facet empty, and no error anywhere.
--
-- Only the two keys are removed; the name stays, because DedupKey is built
-- from it and every writer of a row already agrees on it. Rows with neither
-- key -- everything written since 0024 -- are not touched.
UPDATE concerts
SET data = jsonb_set(data, '{artist}', (data -> 'artist') - 'id' - 'genres')
WHERE jsonb_typeof(data -> 'artist') = 'object'
  AND (data -> 'artist') ?| ARRAY['id', 'genres'];
