-- +goose Up
CREATE TABLE inbox_messages (
    consumer_name TEXT NOT NULL,
    message_id TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    outcome TEXT,
    transaction_id UUID REFERENCES wager_transactions (id),
    CONSTRAINT inbox_messages_pkey PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_messages_completion CHECK ((completed_at IS NULL) = (outcome IS NULL))
);

GRANT SELECT, INSERT, UPDATE ON inbox_messages TO wallet_app;

-- +goose Down
DROP TABLE inbox_messages;
