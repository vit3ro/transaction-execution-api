DROP INDEX IF EXISTS idx_transactions_idempotency_key;

ALTER TABLE IF EXISTS transactions
    DROP COLUMN IF EXISTS idempotency_key;
