-- Artists a user chose by hand on Ticketmaster (CF-30, docs/public-path.md §5).
-- The feed for an account without Spotify is built from these, and a Spotify
-- account's picks go to the front of its scan.
--
-- Everything in a row came from Ticketmaster's attraction search: the ID is
-- what the scan queries events by, so a pick never spends a name-resolution
-- call, and nothing here is Spotify Content.
--
-- No cap in the schema. db.AddArtistPick enforces it (30, see
-- db.MaxArtistPicks) in the same statement as the insert, the way
-- user_subscribed_artists does, so a constant can move without a migration.
CREATE TABLE user_artist_picks (
  user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  attraction_id  TEXT NOT NULL CHECK (attraction_id <> ''),
  name           TEXT NOT NULL,
  -- Ticketmaster's genre for the attraction, when it has one. It becomes the
  -- act's genre on this user's cards and so their genre facet.
  genre          TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, attraction_id)
);
