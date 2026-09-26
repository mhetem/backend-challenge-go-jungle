-- name: InsertInboxMessage :execrows
INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (consumer_name, message_id) DO NOTHING;

-- name: GetInboxMessage :one
SELECT * FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2;

-- name: CompleteInboxMessage :execrows
UPDATE inbox_messages
SET completed_at = $3, outcome = $4, transaction_id = $5
WHERE consumer_name = $1 AND message_id = $2 AND completed_at IS NULL;
