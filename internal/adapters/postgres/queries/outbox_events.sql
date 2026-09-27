-- name: InsertOutboxEvent :exec
INSERT INTO outbox_events (
    id, aggregate_type, aggregate_id, partition_key, event_type, event_version,
    correlation_id, causation_id, payload, occurred_at, next_attempt_at, trace_parent
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ClaimOutboxEvents :many
UPDATE outbox_events
SET claimed_by = $1, claimed_until = $2, attempts = attempts + 1
WHERE id IN (
    SELECT id FROM outbox_events
    WHERE published_at IS NULL
      AND outbox_events.next_attempt_at <= $3
      AND (claimed_until IS NULL OR claimed_until < $3)
    ORDER BY seq
    LIMIT $4
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkOutboxEventPublished :execrows
UPDATE outbox_events
SET published_at = $3, last_error = NULL
WHERE id = $1 AND claimed_by = $2 AND published_at IS NULL;

-- name: ReleaseOutboxEvent :execrows
UPDATE outbox_events
SET claimed_by = NULL, claimed_until = NULL, next_attempt_at = $3, last_error = $4
WHERE id = $1 AND claimed_by = $2 AND published_at IS NULL;

-- name: CountPendingOutboxEvents :one
SELECT count(*) FROM outbox_events WHERE published_at IS NULL;

-- name: OldestPendingOutboxEvent :one
SELECT occurred_at FROM outbox_events WHERE published_at IS NULL ORDER BY seq LIMIT 1;
