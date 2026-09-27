-- name: InsertWallet :exec
INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: UpdateWalletBalance :execrows
UPDATE wallets
SET balance_minor = $3, version = version + 1, updated_at = $4
WHERE id = $1 AND version = $2;

-- name: GetWallet :one
SELECT * FROM wallets WHERE id = $1;

-- name: GetWalletForUpdate :one
SELECT * FROM wallets WHERE id = $1 FOR UPDATE;

-- name: GetWalletByPlayer :one
SELECT * FROM wallets WHERE player_id = $1 AND currency = $2;

-- name: SumWalletBalances :many
SELECT currency, count(*)::bigint AS wallets, round(sum(balance_minor) / 100, 2)::text AS balance
FROM wallets
GROUP BY currency
ORDER BY currency;
