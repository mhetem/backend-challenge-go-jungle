-- +goose Up
CREATE TABLE wager_transactions (
    id UUID PRIMARY KEY,
    origin TEXT NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    wallet_id UUID NOT NULL,
    player_id UUID NOT NULL,
    currency CHAR(3) NOT NULL,
    amount_minor BIGINT NOT NULL,
    provider_id TEXT REFERENCES providers (id),
    external_transaction_id TEXT,
    idempotency_key TEXT,
    payload_hash TEXT,
    round_id TEXT,
    game_id TEXT,
    reference_external_transaction_id TEXT,
    reference_transaction_id UUID REFERENCES wager_transactions (id),
    correlation_id TEXT NOT NULL,
    failure_code TEXT,
    result_balance_minor BIGINT,
    result_wallet_version BIGINT,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    reference_deadline_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CONSTRAINT wager_transactions_origin CHECK (origin IN ('INTERNAL', 'EXTERNAL') AND (origin = 'INTERNAL') = (kind = 'OPENING')),
    CONSTRAINT wager_transactions_kind CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wager_transactions_status CHECK (status IN ('PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wager_transactions_currency_format CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT wager_transactions_zero_policy CHECK (CASE WHEN kind = 'LOSS' THEN amount_minor = 0 ELSE amount_minor > 0 END),
    CONSTRAINT wager_transactions_internal_fields CHECK (
        origin <> 'INTERNAL' OR (
            provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL
            AND status = 'PROCESSED'
        )
    ),
    CONSTRAINT wager_transactions_external_fields CHECK (
        origin <> 'EXTERNAL' OR (
            provider_id IS NOT NULL
            AND coalesce(external_transaction_id, '') ~ '^[!-~]{1,128}$'
            AND coalesce(idempotency_key, '') ~ '^[!-~]{1,128}$'
            AND coalesce(payload_hash, '') ~ '^[0-9a-f]{64}$'
            AND coalesce(round_id, '') ~ '^[!-~]{1,128}$'
            AND coalesce(game_id, '') ~ '^[!-~]{1,128}$'
        )
    ),
    CONSTRAINT wager_transactions_reference_required CHECK (kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wager_transactions_reference_allowed CHECK (kind IN ('WIN', 'REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NULL),
    CONSTRAINT wager_transactions_reference_format CHECK (
        reference_external_transaction_id ~ '^[!-~]{1,128}$' AND reference_external_transaction_id <> external_transaction_id
    ),
    CONSTRAINT wager_transactions_reference_resolved CHECK (reference_transaction_id IS NULL OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wager_transactions_failure_code CHECK ((failure_code IS NOT NULL) = (status IN ('REJECTED', 'FAILED'))),
    CONSTRAINT wager_transactions_result CHECK (
        (result_balance_minor IS NULL) = (result_wallet_version IS NULL)
        AND (status <> 'PROCESSED' OR result_balance_minor IS NOT NULL)
        AND (result_balance_minor IS NULL OR status IN ('PROCESSED', 'REJECTED'))
        AND (result_balance_minor IS NULL OR result_balance_minor >= 0)
        AND (result_wallet_version IS NULL OR result_wallet_version >= 1)
    ),
    CONSTRAINT wager_transactions_attempts CHECK (attempts >= 0),
    CONSTRAINT wager_transactions_pending_reference CHECK (
        status <> 'PENDING_REFERENCE' OR (attempts >= 1 AND next_attempt_at IS NOT NULL AND reference_deadline_at IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_completed_at CHECK ((completed_at IS NOT NULL) = (status IN ('PROCESSED', 'REJECTED', 'FAILED'))),
    CONSTRAINT wager_transactions_external_id_key UNIQUE (provider_id, external_transaction_id),
    CONSTRAINT wager_transactions_idempotency_key UNIQUE (provider_id, idempotency_key),
    CONSTRAINT wager_transactions_ledger_key UNIQUE (id, wallet_id, currency, amount_minor)
);

CREATE UNIQUE INDEX wager_transactions_one_opening ON wager_transactions (wallet_id) WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wager_transactions_one_reversal ON wager_transactions (reference_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

CREATE INDEX wager_transactions_due ON wager_transactions (next_attempt_at) WHERE status = 'PENDING_REFERENCE';

CREATE INDEX wager_transactions_awaiting_reference ON wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PENDING_REFERENCE';

GRANT SELECT, INSERT, UPDATE ON wager_transactions TO wallet_app;

-- +goose Down
DROP TABLE wager_transactions;
