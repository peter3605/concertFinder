-- Fails, and changes nothing, once any account has no Spotify ID. That is
-- deliberate: such an account cannot be represented by the old schema, and
-- deleting it to make a rollback succeed would destroy a user's data. Down
-- migrations are run by hand, so the transaction is explicit here: without it,
-- a psql session would carry on past the failed ALTER and drop the table.
BEGIN;
ALTER TABLE users ALTER COLUMN spotify_user_id SET NOT NULL;
DROP TABLE user_identities;
COMMIT;
