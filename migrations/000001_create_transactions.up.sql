CREATE TABLE transactions (
    id                      UUID PRIMARY KEY,
    account_id              TEXT NOT NULL,
    type                    TEXT NOT NULL,
    amount                  NUMERIC(18, 2) NOT NULL,
    currency                VARCHAR(3) NOT NULL,
    description             VARCHAR(255) NOT NULL DEFAULT '',
    status                  TEXT NOT NULL,
    provider_transaction_id TEXT,
    balance_after           NUMERIC(18, 2),
    rejection_code          TEXT,
    rejection_message       TEXT,
    created_at              TIMESTAMPTZ NOT NULL,
    updated_at              TIMESTAMPTZ NOT NULL,

    -- CHECK constraints instead of ENUM types: adding a status later is a simple
    -- migration, and the database rejects bad rows even if the service has a bug.
    CONSTRAINT transactions_account_id_check CHECK (account_id <> ''),
    CONSTRAINT transactions_type_check CHECK (type IN ('CREDIT', 'DEBIT')),
    CONSTRAINT transactions_status_check CHECK (status IN ('PENDING', 'EXECUTED', 'REJECTED', 'FAILED')),
    CONSTRAINT transactions_amount_check CHECK (amount > 0)
);

-- All three indexes end in (created_at DESC, id DESC), the same order as the
-- list query, so rows are read already sorted and the scan stops at LIMIT.
--
-- Main query: the history of one account. Also covers account + status or type.
CREATE INDEX idx_transactions_account_created
    ON transactions (account_id, created_at DESC, id DESC);

-- Listing by status, and finding old PENDING rows for reconciliation.
CREATE INDEX idx_transactions_status_created
    ON transactions (status, created_at DESC, id DESC);

-- Unfiltered list and the type-only filter. There is no index on type: with
-- two values it filters almost nothing and every index slows down inserts.
CREATE INDEX idx_transactions_created
    ON transactions (created_at DESC, id DESC);
