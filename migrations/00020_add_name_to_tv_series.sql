-- +goose Up
-- +goose StatementBegin
ALTER TABLE tv_series
    ADD COLUMN name TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tv_series
    DROP COLUMN IF EXISTS name;
-- +goose StatementEnd
