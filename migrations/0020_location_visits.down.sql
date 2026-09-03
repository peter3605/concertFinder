-- Daily counters with no meaning once their day has passed, so dropping the
-- table loses nothing that could not be reconstructed by living another day.
DROP TABLE IF EXISTS user_location_visits;
