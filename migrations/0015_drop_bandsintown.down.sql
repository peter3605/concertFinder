-- Restores the column, not its contents: the names were derived from Spotify
-- artist names at resolution time and are recoverable by a rescan, whereas
-- the deleted rate_ledger rows are daily counters with no meaning once their
-- day has passed.
ALTER TABLE artist_resolutions ADD COLUMN IF NOT EXISTS bandsintown_name TEXT;
