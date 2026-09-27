-- +goose Up
ALTER TABLE outbox_events ADD COLUMN trace_parent TEXT
    CONSTRAINT outbox_events_trace_parent_format CHECK (trace_parent ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION outbox_events_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.seq, NEW.aggregate_type, NEW.aggregate_id, NEW.partition_key, NEW.event_type,
        NEW.event_version, NEW.correlation_id, NEW.causation_id, NEW.payload, NEW.occurred_at, NEW.trace_parent)
        IS DISTINCT FROM
       (OLD.id, OLD.seq, OLD.aggregate_type, OLD.aggregate_id, OLD.partition_key, OLD.event_type,
        OLD.event_version, OLD.correlation_id, OLD.causation_id, OLD.payload, OLD.occurred_at, OLD.trace_parent) THEN
        RAISE EXCEPTION 'outbox event % is an immutable snapshot', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'outbox_events_immutable';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION outbox_events_guard() RETURNS trigger LANGUAGE plpgsql AS $$
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

ALTER TABLE outbox_events DROP COLUMN trace_parent;
