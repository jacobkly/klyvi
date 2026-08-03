-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN birthday DATE NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users
    DROP COLUMN IF EXISTS birthday;
-- +goose StatementEnd
