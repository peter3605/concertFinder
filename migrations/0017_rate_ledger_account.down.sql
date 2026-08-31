-- The rows are daily counters with no meaning once their day has passed, so
-- dropping the table loses nothing that could be reconstructed anyway.
DROP TABLE IF EXISTS rate_ledger_account;
