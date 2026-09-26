-- +goose Up
CREATE TABLE ledger_entries (
    id UUID PRIMARY KEY,
    wallet_id UUID NOT NULL,
    transaction_id UUID NOT NULL,
    direction TEXT NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency CHAR(3) NOT NULL,
    balance_before_minor BIGINT NOT NULL,
    balance_after_minor BIGINT NOT NULL,
    wallet_version BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ledger_entries_direction CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT ledger_entries_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT ledger_entries_balances_non_negative CHECK (balance_before_minor >= 0 AND balance_after_minor >= 0),
    CONSTRAINT ledger_entries_arithmetic CHECK (
        CASE WHEN direction = 'DEBIT'
            THEN balance_after_minor = balance_before_minor - amount_minor
            ELSE balance_after_minor = balance_before_minor + amount_minor
        END
    ),
    CONSTRAINT ledger_entries_version_positive CHECK (wallet_version >= 1),
    CONSTRAINT ledger_entries_wallet_fkey FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency),
    CONSTRAINT ledger_entries_transaction_fkey FOREIGN KEY (transaction_id, wallet_id, currency, amount_minor)
        REFERENCES wager_transactions (id, wallet_id, currency, amount_minor),
    CONSTRAINT ledger_entries_wallet_transaction_key UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_entries_wallet_version_key UNIQUE (wallet_id, wallet_version)
);

GRANT SELECT, INSERT ON ledger_entries TO wallet_app;

-- +goose Down
DROP TABLE ledger_entries;
