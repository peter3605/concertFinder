-- Reversal drops the column and its index. Nothing is recoverable from it --
-- the raw tokens were never written after 0018 and cannot be derived from
-- their hashes -- so rolling back logs everyone out, exactly as rolling
-- forward did.
DROP INDEX IF EXISTS sessions_token_hash_idx;
ALTER TABLE sessions
  DROP COLUMN IF EXISTS token_hash;
