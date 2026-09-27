-- name: InsertLedgerEntry :exec
INSERT INTO ledger_entries (
    id, wallet_id, transaction_id, direction, amount_minor, currency,
    balance_before_minor, balance_after_minor, wallet_version, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: ListLedgerEntries :many
SELECT * FROM ledger_entries
WHERE wallet_id = $1 AND wallet_version > $2
ORDER BY wallet_version
LIMIT $3;

-- name: SummarizeLedger :one
SELECT
    count(*)::bigint AS entries,
    coalesce(min(wallet_version), 0)::bigint AS first_version,
    coalesce(max(wallet_version), 0)::bigint AS last_version,
    round(coalesce(sum(CASE WHEN direction = 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0) / 100, 2)::text AS net
FROM ledger_entries
WHERE wallet_id = $1;
