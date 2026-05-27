-- +goose Up
-- +goose StatementBegin
-- Replace the existing UNIQUE(id, season_number, media_type) constraint on
-- media_index with one that uses NULLS NOT DISTINCT. The original constraint
-- (created in 00009) treats NULL season_number as distinct from another NULL,
-- so concurrent first-fetches of the same movie can insert duplicate index
-- rows because the ON CONFLICT clause in EnsureMediaIndex never fires.
DO $$
DECLARE
    cn TEXT;
BEGIN
    FOR cn IN
        SELECT conname
        FROM pg_constraint c
        JOIN pg_class t ON t.oid = c.conrelid
        WHERE t.relname = 'media_index' AND c.contype = 'u'
    LOOP
        EXECUTE 'ALTER TABLE media_index DROP CONSTRAINT ' || quote_ident(cn);
    END LOOP;
END $$;

ALTER TABLE media_index
    ADD CONSTRAINT media_index_id_season_number_media_type_key
    UNIQUE NULLS NOT DISTINCT (id, season_number, media_type);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_index
    DROP CONSTRAINT IF EXISTS media_index_id_season_number_media_type_key;
ALTER TABLE media_index
    ADD CONSTRAINT media_index_tmdb_id_season_number_media_type_key
    UNIQUE (id, season_number, media_type);
-- +goose StatementEnd
