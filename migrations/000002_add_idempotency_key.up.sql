ALTER TABLE transactions
    ADD COLUMN idempotency_key VARCHAR(255);

-- Partial unique index: one transaction per key, and rows without a key are
-- not indexed. This is what makes two concurrent requests safe.
CREATE UNIQUE INDEX idx_transactions_idempotency_key
    ON transactions (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
