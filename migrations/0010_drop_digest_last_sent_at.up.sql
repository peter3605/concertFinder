-- digest_last_sent_at was the pre-audit-fix mechanism for tracking what a
-- user had been emailed. Post-audit we moved to user_digest_sent, which
-- records the exact dedup_keys sent — a strict superset of what the
-- timestamp told us. The column is now vestigial.
ALTER TABLE users DROP COLUMN IF EXISTS digest_last_sent_at;
