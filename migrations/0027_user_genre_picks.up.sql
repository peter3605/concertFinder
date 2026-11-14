-- Genres a user chose by hand (CF-31, docs/public-path.md §1.2, §2, §5).
--
-- Top-level Ticketmaster Music genres only: sub-genres were measured too
-- sparse to offer (Indie Rock returned 2 New York events; American Punk,
-- Bebop and Acid House returned 0). Everything in a row came from
-- Ticketmaster's classification list, never from a client.
--
-- No cap in the schema. db.AddGenrePick enforces it (5, db.MaxGenrePicks) in
-- the same statement as the insert, as user_artist_picks does.
CREATE TABLE user_genre_picks (
  user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  genre_id    TEXT NOT NULL CHECK (genre_id <> ''),
  name        TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, genre_id)
);

-- A genre listing is a date-sorted prefix: fetched date ascending and stopped
-- at three pages, because New York jazz alone is 1,590 events. That makes it
-- the one cached Ticketmaster listing that is deliberately truncated, and
-- horizon is what makes the truncation honest: the venue-local day
-- ("2006-01-02") of the first date the listing does NOT fully cover. NULL
-- means the listing is complete. Only genre rows set it.
ALTER TABLE concert_cache ADD COLUMN horizon TEXT;

-- The horizon of the genre part of a user's feed, so the feed can say "shows
-- before <day>" rather than imply it holds everything. NULL when the user has
-- no genre picks or every listing was complete.
ALTER TABLE user_concert_snapshots ADD COLUMN genre_horizon TEXT;
