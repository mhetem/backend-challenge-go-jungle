-- +goose Up
CREATE TABLE outbox_events (
    id UUID PRIMARY KEY,
    seq BIGINT GENERATED ALWAYS AS IDENTITY,
    aggregate_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    partition_key UUID NOT NULL,
    event_type TEXT NOT NULL,
    event_version INTEGER NOT NULL,
    correlation_id TEXT NOT NULL,
    causation_id TEXT,
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    claimed_by TEXT,
    claimed_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    last_error TEXT,
    CONSTRAINT outbox_events_seq_key UNIQUE (seq),
    CONSTRAINT outbox_events_version_positive CHECK (event_version >= 1),
    CONSTRAINT outbox_events_attempts CHECK (attempts >= 0),
    CONSTRAINT outbox_events_claim CHECK ((claimed_by IS NULL) = (claimed_until IS NULL))
);

CREATE INDEX outbox_events_due ON outbox_events (next_attempt_at) WHERE published_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION outbox_events_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.seq, NEW.aggregate_type, NEW.aggregate_id, NEW.partition_key, NEW.event_type,
        NEW.event_version, NEW.correlation_id, NEW.causation_id, NEW.payload, NEW.occurred_at)
        IS DISTINCT FROM
       (OLD.id, OLD.seq, OLD.aggregate_type, OLD.aggregate_id, OLD.partition_key, OLD.event_type,
        OLD.event_version, OLD.correlation_id, OLD.causation_id, OLD.payload, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox event % is an immutable snapshot', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'outbox_events_immutable';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER outbox_events_guard BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_events_guard();

GRANT SELECT, INSERT, UPDATE ON outbox_events TO wallet_app;

-- +goose Down
DROP TABLE outbox_events;
DROP FUNCTION outbox_events_guard();
