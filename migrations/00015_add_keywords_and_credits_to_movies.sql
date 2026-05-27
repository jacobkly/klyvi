-- +goose Up
-- +goose StatementBegin
ALTER TABLE movies
    ADD COLUMN keywords JSONB,
    ADD COLUMN credits  JSONB;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE movies
    DROP COLUMN IF EXISTS keywords,
    DROP COLUMN IF EXISTS credits;
-- +goose StatementEnd
