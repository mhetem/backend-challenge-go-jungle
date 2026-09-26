-- name: InsertLedgerEntry :exec
INSERT INTO ledger_entries (
    id, wallet_id, transaction_id, direction, amount_minor, currency,
    balance_before_minor, balance_after_minor, wallet_version, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);
