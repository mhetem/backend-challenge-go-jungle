-- name: InsertLedgerPosting :exec
INSERT INTO ledger_postings (wallet_id, transaction_id, account, direction, amount_minor, currency, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: SummarizeWalletPostings :one
SELECT
    count(*)::bigint AS postings,
    round(coalesce(sum(CASE WHEN direction = 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0) / 100, 2)::text AS net
FROM ledger_postings
WHERE wallet_id = $1 AND account = 'PLAYER_BALANCES';

-- name: TrialBalance :many
SELECT
    currency,
    account,
    count(*)::bigint AS postings,
    round(coalesce(sum(amount_minor) FILTER (WHERE direction = 'DEBIT'), 0) / 100, 2)::text AS debits,
    round(coalesce(sum(amount_minor) FILTER (WHERE direction = 'CREDIT'), 0) / 100, 2)::text AS credits
FROM ledger_postings
GROUP BY currency, account
ORDER BY currency, account;
