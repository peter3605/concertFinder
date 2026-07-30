-- official_url was the per-Spotify-artist-ID cache of MusicBrainz's
-- "official homepage" link. mb_url_cache now stores the same information
-- keyed on lowercased artist name and is the source of truth (with a
-- proper LRU'd in-memory hot layer in front of it). Dropping the column
-- eliminates the duplication.
ALTER TABLE artist_resolutions DROP COLUMN IF EXISTS official_url;
