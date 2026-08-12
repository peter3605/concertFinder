-- Bandsintown is gone as a source. Its public API returned an AWS
-- "explicit deny in an identity-based policy" 403 on every request for the
-- entire period it was wired up, and the partnership request went
-- unanswered, so the column below has been carrying the artist name used to
-- build calls nothing has been able to make.
--
-- Ticketmaster is now the only primary source; the Phase 2 fallback chain
-- (MusicBrainz -> official artist site -> JSON-LD) is the only secondary.
ALTER TABLE artist_resolutions DROP COLUMN IF EXISTS bandsintown_name;

-- Free the per-user quota rows for the retired source. These are daily
-- counters, so there is nothing here worth keeping — and leaving them means
-- rate.AllSources no longer matches what the table contains, which makes
-- the ledger's own diagnostics misleading.
DELETE FROM rate_ledger WHERE source = 'bandsintown';

-- NOTE: concerts.data blobs written before this migration still contain
-- ticket links with "source":"bandsintown". They are left alone deliberately
-- — they are valid URLs to real shows, and they age out on their own as the
-- janitor prunes past events and nightly scans rewrite snapshots.
-- concerts.priorityOf keeps them sorting below Ticketmaster in the meantime.
