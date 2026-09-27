-- +goose Up
CREATE INDEX outbox_events_pending ON outbox_events (seq) WHERE published_at IS NULL;
DROP INDEX outbox_events_due;

-- +goose Down
CREATE INDEX outbox_events_due ON outbox_events (next_attempt_at) WHERE published_at IS NULL;
DROP INDEX outbox_events_pending;
