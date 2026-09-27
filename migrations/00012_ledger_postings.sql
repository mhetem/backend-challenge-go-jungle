-- +goose Up
CREATE TABLE ledger_postings (
    wallet_id UUID NOT NULL,
    transaction_id UUID NOT NULL,
    account TEXT NOT NULL,
    direction TEXT NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency CHAR(3) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ledger_postings_pkey PRIMARY KEY (wallet_id, transaction_id, account),
    CONSTRAINT ledger_postings_account CHECK (account IN ('FUNDING', 'PLAYER_BALANCES', 'GAMING_REVENUE')),
    CONSTRAINT ledger_postings_direction CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT ledger_postings_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT ledger_postings_transaction_fkey FOREIGN KEY (transaction_id, wallet_id, currency, amount_minor)
        REFERENCES wager_transactions (id, wallet_id, currency, amount_minor)
);

INSERT INTO ledger_postings (wallet_id, transaction_id, account, direction, amount_minor, currency, created_at)
SELECT wallet_id, transaction_id, 'PLAYER_BALANCES', direction, amount_minor, currency, created_at
FROM ledger_entries
UNION ALL
SELECT e.wallet_id, e.transaction_id,
    CASE WHEN t.kind = 'OPENING' THEN 'FUNDING' ELSE 'GAMING_REVENUE' END,
    CASE WHEN e.direction = 'DEBIT' THEN 'CREDIT' ELSE 'DEBIT' END,
    e.amount_minor, e.currency, e.created_at
FROM ledger_entries e
JOIN wager_transactions t ON t.id = e.transaction_id;

-- +goose StatementBegin
CREATE FUNCTION ledger_postings_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger_postings is append-only: % refused', TG_OP
        USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'ledger_postings_append_only';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER ledger_postings_append_only BEFORE UPDATE OR DELETE ON ledger_postings
    FOR EACH ROW EXECUTE FUNCTION ledger_postings_append_only();

CREATE TRIGGER ledger_postings_no_truncate BEFORE TRUNCATE ON ledger_postings
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_postings_append_only();

-- +goose StatementBegin
CREATE FUNCTION ledger_journal_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    tx_kind TEXT;
    tx_status TEXT;
    debits BIGINT;
    credits BIGINT;
BEGIN
    SELECT kind, status INTO tx_kind, tx_status FROM wager_transactions WHERE id = NEW.transaction_id;
    IF tx_status IS DISTINCT FROM 'PROCESSED' THEN
        RAISE EXCEPTION 'transaction % is % and cannot move money', NEW.transaction_id, tx_status
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'ledger_journal_processed';
    END IF;
    SELECT coalesce(sum(amount_minor) FILTER (WHERE direction = 'DEBIT'), 0),
           coalesce(sum(amount_minor) FILTER (WHERE direction = 'CREDIT'), 0)
    INTO debits, credits
    FROM ledger_postings
    WHERE wallet_id = NEW.wallet_id AND transaction_id = NEW.transaction_id;
    IF debits = 0 OR debits <> credits THEN
        RAISE EXCEPTION 'journal of transaction % is unbalanced: debits %, credits %', NEW.transaction_id, debits, credits
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'ledger_journal_balanced';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM ledger_postings p
        JOIN ledger_entries e ON e.wallet_id = p.wallet_id AND e.transaction_id = p.transaction_id AND e.direction = p.direction
        WHERE p.wallet_id = NEW.wallet_id AND p.transaction_id = NEW.transaction_id AND p.account = 'PLAYER_BALANCES'
    ) OR NOT EXISTS (
        SELECT 1 FROM ledger_postings
        WHERE wallet_id = NEW.wallet_id AND transaction_id = NEW.transaction_id
          AND account = CASE WHEN tx_kind = 'OPENING' THEN 'FUNDING' ELSE 'GAMING_REVENUE' END
    ) THEN
        RAISE EXCEPTION 'journal of % transaction % must mirror its wallet ledger entry against the % account',
            tx_kind, NEW.transaction_id, CASE WHEN tx_kind = 'OPENING' THEN 'FUNDING' ELSE 'GAMING_REVENUE' END
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'ledger_journal_accounts';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER ledger_postings_journal AFTER INSERT ON ledger_postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_journal_check();

CREATE CONSTRAINT TRIGGER ledger_entries_journal AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_journal_check();

GRANT SELECT, INSERT ON ledger_postings TO wallet_app;

-- +goose Down
DROP TRIGGER ledger_entries_journal ON ledger_entries;
DROP TABLE ledger_postings;
DROP FUNCTION ledger_journal_check();
DROP FUNCTION ledger_postings_append_only();
