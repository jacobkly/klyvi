-- +goose Up
-- +goose StatementBegin
-- jsonb_id_array extracts the `id` values from a JSONB array of {id, name}
-- objects into an int[]. IMMUTABLE so it can back both a generated column and
-- an index. A NULL/absent array yields an empty int[] (never NULL), so array
-- overlap (&&) against it is a clean false rather than NULL.
CREATE OR REPLACE FUNCTION jsonb_id_array(j JSONB) RETURNS INT[]
    LANGUAGE sql
    IMMUTABLE
    PARALLEL SAFE
AS $$
    SELECT COALESCE(array_agg((elem ->> 'id')::int), '{}')
    FROM jsonb_array_elements(COALESCE(j, '[]'::jsonb)) elem
    WHERE (elem ->> 'id') IS NOT NULL
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Generated int[] projections of the genre/keyword id lists so the
-- recommender can retrieve taste-relevant candidates via array overlap
-- instead of scoring the globally most-voted movies. STORED so existing rows
-- backfill on ALTER and future inserts populate automatically.
ALTER TABLE movies
    ADD COLUMN genre_ids   INT[] GENERATED ALWAYS AS (jsonb_id_array(genres))   STORED,
    ADD COLUMN keyword_ids INT[] GENERATED ALWAYS AS (jsonb_id_array(keywords)) STORED;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_movies_genre_ids ON movies USING gin (genre_ids);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_movies_keyword_ids ON movies USING gin (keyword_ids);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_movies_keyword_ids;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_movies_genre_ids;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE movies
    DROP COLUMN IF EXISTS genre_ids,
    DROP COLUMN IF EXISTS keyword_ids;
-- +goose StatementEnd

-- +goose StatementBegin
DROP FUNCTION IF EXISTS jsonb_id_array(JSONB);
-- +goose StatementEnd
