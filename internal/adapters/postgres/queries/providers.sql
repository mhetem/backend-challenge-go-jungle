-- name: ProviderExists :one
SELECT EXISTS (SELECT 1 FROM providers WHERE id = $1);
