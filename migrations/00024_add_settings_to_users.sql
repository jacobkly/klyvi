-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN settings JSONB NOT NULL DEFAULT '{}'::JSONB;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users
    DROP COLUMN IF EXISTS settings;
-- +goose StatementEnd
