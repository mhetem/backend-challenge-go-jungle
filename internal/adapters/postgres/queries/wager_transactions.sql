-- name: InsertTransaction :exec
INSERT INTO wager_transactions (
    id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
    provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
    reference_external_transaction_id, reference_transaction_id, correlation_id, failure_code,
    result_balance_minor, result_wallet_version, attempts, next_attempt_at, reference_deadline_at,
    created_at, updated_at, completed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8,
    $9, $10, $11, $12, $13, $14,
    $15, $16, $17, $18,
    $19, $20, $21, $22, $23,
    $24, $25, $26
);

-- name: GetTransaction :one
SELECT * FROM wager_transactions WHERE id = $1;

-- name: GetTransactionByIdempotencyKey :one
SELECT * FROM wager_transactions WHERE provider_id = $1 AND idempotency_key = $2;

-- name: GetTransactionByExternalID :one
SELECT * FROM wager_transactions WHERE provider_id = $1 AND external_transaction_id = $2;

-- name: UpdateTransactionState :execrows
UPDATE wager_transactions
SET status = $2, failure_code = $3, reference_transaction_id = $4, result_balance_minor = $5,
    result_wallet_version = $6, attempts = $7, next_attempt_at = $8, reference_deadline_at = $9,
    updated_at = $10, completed_at = $11
WHERE id = $1 AND status = 'PENDING_REFERENCE';

-- name: WakeDependents :execrows
UPDATE wager_transactions
SET next_attempt_at = $4
WHERE provider_id = $1 AND reference_external_transaction_id = $2 AND wallet_id = $3
  AND status = 'PENDING_REFERENCE' AND next_attempt_at > $4;

-- name: IsTransactionReversed :one
SELECT EXISTS (
    SELECT 1 FROM wager_transactions
    WHERE reference_transaction_id = $1 AND status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK')
);
