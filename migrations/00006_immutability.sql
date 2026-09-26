-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION ledger_entries_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger_entries is append-only: % refused', TG_OP
        USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'ledger_entries_append_only';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER ledger_entries_append_only BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_append_only();

CREATE TRIGGER ledger_entries_no_truncate BEFORE TRUNCATE ON ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_entries_append_only();

REVOKE UPDATE, DELETE, TRUNCATE ON ledger_entries FROM wallet_app;

-- +goose StatementBegin
CREATE FUNCTION wager_transactions_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wager transaction % cannot be deleted', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'wager_transactions_append_only';
    END IF;
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager transaction % is % and cannot change', OLD.id, OLD.status
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'wager_transactions_terminal';
    END IF;
    IF (NEW.id, NEW.origin, NEW.kind, NEW.wallet_id, NEW.player_id, NEW.currency, NEW.amount_minor,
        NEW.provider_id, NEW.external_transaction_id, NEW.idempotency_key, NEW.payload_hash, NEW.round_id,
        NEW.game_id, NEW.reference_external_transaction_id, NEW.correlation_id, NEW.created_at)
        IS DISTINCT FROM
       (OLD.id, OLD.origin, OLD.kind, OLD.wallet_id, OLD.player_id, OLD.currency, OLD.amount_minor,
        OLD.provider_id, OLD.external_transaction_id, OLD.idempotency_key, OLD.payload_hash, OLD.round_id,
        OLD.game_id, OLD.reference_external_transaction_id, OLD.correlation_id, OLD.created_at)
       OR (OLD.reference_transaction_id IS NOT NULL AND NEW.reference_transaction_id IS DISTINCT FROM OLD.reference_transaction_id) THEN
        RAISE EXCEPTION 'wager transaction % identity, money and hash are immutable', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'wager_transactions_immutable_fields';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER wager_transactions_guard BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_guard();

-- +goose StatementBegin
CREATE FUNCTION wallets_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wallet % cannot be deleted', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'wallets_append_only';
    END IF;
    IF (NEW.id, NEW.player_id, NEW.currency, NEW.created_at) IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallet % identity is immutable', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'wallets_immutable_fields';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER wallets_guard BEFORE UPDATE OR DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallets_guard();

-- +goose Down
DROP TRIGGER wallets_guard ON wallets;
DROP FUNCTION wallets_guard();
DROP TRIGGER wager_transactions_guard ON wager_transactions;
DROP FUNCTION wager_transactions_guard();
DROP TRIGGER ledger_entries_no_truncate ON ledger_entries;
DROP TRIGGER ledger_entries_append_only ON ledger_entries;
DROP FUNCTION ledger_entries_append_only();
