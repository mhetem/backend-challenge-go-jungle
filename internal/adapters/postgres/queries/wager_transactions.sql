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
