-- +goose Up
INSERT INTO providers (id, name) VALUES
    ('provider-a', 'Provider A'),
    ('provider-b', 'Provider B');

-- +goose Down
DELETE FROM providers WHERE id IN ('provider-a', 'provider-b');
