-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_list
    ADD COLUMN favorite BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_list
    DROP COLUMN IF EXISTS favorite;
-- +goose StatementEnd
