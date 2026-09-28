-- +goose Up
-- +goose StatementBegin
-- A series becomes a recommendation unit with its own media_index identity:
-- media_type 'tv', id = tv_id, season_number NULL. Seasons stay the tracking
-- unit, so media_list keeps ('movie', 'season').
ALTER TABLE media_index DROP CONSTRAINT media_index_media_type_check;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE media_index ADD CONSTRAINT media_index_media_type_check
    CHECK (media_type IN ('movie', 'season', 'tv'));
-- +goose StatementEnd

-- +goose StatementBegin
-- Only a season carries a season number. A series row with one would slip past
-- the NULLS NOT DISTINCT uniqueness (00014), and a season without one could
-- not be resolved back to its tv_seasons row.
ALTER TABLE media_index ADD CONSTRAINT media_index_season_number_shape
    CHECK ((media_type = 'season') = (season_number IS NOT NULL));
-- +goose StatementEnd

-- +goose StatementBegin
-- Feed signal (impressions, clicks, dismissals, saves) can target a series.
ALTER TABLE interactions DROP CONSTRAINT interactions_media_type_check;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE interactions ADD CONSTRAINT interactions_media_type_check
    CHECK (media_type IN ('movie', 'season', 'tv'));
-- +goose StatementEnd

-- +goose StatementBegin
-- Generated id arrays for series, mirroring movies (00027), so TV candidates
-- come from GIN overlap retrieval. Genre ids are TMDB's TV ids as-is; the
-- recommender maps TV's combined genres onto movie ids when it reads them.
-- The normalizer stores keywords as a bare array, but a row cached before it
-- unwrapped TMDB's {"results": [...]} envelope is read through that too.
-- Anything else (SQL or JSON null) projects to '{}'.
ALTER TABLE tv_series
    ADD COLUMN genre_ids INT[] GENERATED ALWAYS AS (jsonb_id_array(
        CASE WHEN jsonb_typeof(genres) = 'array' THEN genres END
    )) STORED,
    ADD COLUMN keyword_ids INT[] GENERATED ALWAYS AS (jsonb_id_array(
        CASE
            WHEN jsonb_typeof(keywords) = 'array' THEN keywords
            WHEN jsonb_typeof(keywords -> 'results') = 'array' THEN keywords -> 'results'
        END
    )) STORED;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_tv_series_genre_ids ON tv_series USING gin (genre_ids);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_tv_series_keyword_ids ON tv_series USING gin (keyword_ids);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_tv_series_keyword_ids;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_tv_series_genre_ids;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE tv_series
    DROP COLUMN IF EXISTS genre_ids,
    DROP COLUMN IF EXISTS keyword_ids;
-- +goose StatementEnd

-- +goose StatementBegin
-- Restoring the narrower checks fails while 'tv' rows exist. That is on
-- purpose: series rows and the signal on them are data, so a rollback never
-- deletes them silently. Remove them by hand first if the rollback is meant.
ALTER TABLE interactions DROP CONSTRAINT interactions_media_type_check;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE interactions ADD CONSTRAINT interactions_media_type_check
    CHECK (media_type IN ('movie', 'season'));
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE media_index DROP CONSTRAINT IF EXISTS media_index_season_number_shape;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE media_index DROP CONSTRAINT media_index_media_type_check;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE media_index ADD CONSTRAINT media_index_media_type_check
    CHECK (media_type IN ('movie', 'season'));
-- +goose StatementEnd
