-- name: InsertOutboxEvent :exec
INSERT INTO outbox_events (
    id, aggregate_type, aggregate_id, partition_key, event_type, event_version,
    correlation_id, causation_id, payload, occurred_at, next_attempt_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);
