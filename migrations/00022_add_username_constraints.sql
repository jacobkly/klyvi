-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN username_changed_at TIMESTAMPTZ NULL;

-- Case-insensitive uniqueness. The original UNIQUE constraint lets "Jacob"
-- and "jacob" coexist, which reads as two accounts pretending to be one
-- person. Replace it with a functional unique index on lower(username).
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_username_key;

CREATE UNIQUE INDEX users_username_lower_key
    ON users (lower(username));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS users_username_lower_key;
ALTER TABLE users ADD CONSTRAINT users_username_key UNIQUE (username);
ALTER TABLE users DROP COLUMN IF EXISTS username_changed_at;
-- +goose StatementEnd
