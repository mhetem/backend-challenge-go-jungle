-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION wallets_ledger_coupling() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    before_balance BIGINT := 0;
    before_version BIGINT := 0;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        before_balance := OLD.balance_minor;
        before_version := OLD.version;
    END IF;
    IF NEW.balance_minor = before_balance THEN
        IF NEW.version = before_version OR (TG_OP = 'INSERT' AND NEW.version = 1) THEN
            RETURN NULL;
        END IF;
        RAISE EXCEPTION 'wallet % moved from version % to % without a balance change', NEW.id, before_version, NEW.version
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'wallets_ledger_coupling';
    END IF;
    IF NEW.version <> before_version + 1 OR NOT EXISTS (
        SELECT 1 FROM ledger_entries
        WHERE wallet_id = NEW.id
          AND wallet_version = NEW.version
          AND balance_before_minor = before_balance
          AND balance_after_minor = NEW.balance_minor
    ) THEN
        RAISE EXCEPTION 'wallet % changed balance from % to % at version % without a matching ledger entry',
            NEW.id, before_balance, NEW.balance_minor, NEW.version
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'wallets_ledger_coupling';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER wallets_ledger_coupling AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallets_ledger_coupling();

-- +goose StatementBegin
CREATE FUNCTION ledger_entries_wallet_coupling() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM wallets WHERE id = NEW.wallet_id AND version >= NEW.wallet_version) THEN
        RAISE EXCEPTION 'ledger entry % is at version % but wallet % never reached it', NEW.id, NEW.wallet_version, NEW.wallet_id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'ledger_entries_wallet_coupling';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER ledger_entries_wallet_coupling AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_wallet_coupling();

-- +goose Down
DROP TRIGGER ledger_entries_wallet_coupling ON ledger_entries;
DROP FUNCTION ledger_entries_wallet_coupling();
DROP TRIGGER wallets_ledger_coupling ON wallets;
DROP FUNCTION wallets_ledger_coupling();
