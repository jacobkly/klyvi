-- +goose Up
-- +goose StatementBegin
-- unmatched_titles stores the {title, year} rows an import could not resolve
-- to a catalog film, so the client can list them and let the user find/rate
-- them manually. Defaults to '[]' so existing rows and the JSON response are
-- an empty array, never null.
ALTER TABLE imports
    ADD COLUMN unmatched_titles JSONB NOT NULL DEFAULT '[]'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE imports
    DROP COLUMN IF EXISTS unmatched_titles;
-- +goose StatementEnd
