-- +goose Up
-- +goose StatementBegin
CREATE TABLE taste_profiles (
    user_id              UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    genre_weights        JSONB DEFAULT '{}'::JSONB,        -- {genre_id_string: float}
    keyword_weights      JSONB DEFAULT '{}'::JSONB,        -- {keyword_id_string: float}
    era_weights          JSONB DEFAULT '{}'::JSONB,        -- {decade_string: float, e.g. "1990": 0.42}
    quality_sensitivity  DOUBLE PRECISION DEFAULT 0,        -- how much vote_average influences this user
    liked_count          INTEGER DEFAULT 0,                 -- positive-signal item count
    disliked_count       INTEGER DEFAULT 0,                 -- negative-signal item count
    updated_at           TIMESTAMPTZ DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS taste_profiles;
-- +goose StatementEnd
