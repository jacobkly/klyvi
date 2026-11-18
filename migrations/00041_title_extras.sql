-- +goose Up
-- +goose StatementBegin
-- The age rating, trailer and (for TV) IMDb id a title page shows. They come
-- with the detail fetch. has_extras marks a row fetched with them, so a row
-- cached before this migration fills on its next refresh or its first view.
ALTER TABLE movies
    ADD COLUMN certification TEXT,
    ADD COLUMN trailer_key TEXT,
    ADD COLUMN has_extras BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE tv_series
    ADD COLUMN certification TEXT,
    ADD COLUMN trailer_key TEXT,
    ADD COLUMN imdb_id TEXT,
    ADD COLUMN has_extras BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tv_series
    DROP COLUMN has_extras,
    DROP COLUMN imdb_id,
    DROP COLUMN trailer_key,
    DROP COLUMN certification;

ALTER TABLE movies
    DROP COLUMN has_extras,
    DROP COLUMN trailer_key,
    DROP COLUMN certification;
-- +goose StatementEnd
