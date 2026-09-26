-- +goose Up
CREATE TABLE providers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT providers_id_format CHECK (id ~ '^[!-~]{1,128}$')
);

GRANT SELECT ON providers TO wallet_app;

-- +goose Down
DROP TABLE providers;
