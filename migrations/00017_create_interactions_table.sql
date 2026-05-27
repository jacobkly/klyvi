-- +goose Up
-- +goose StatementBegin
CREATE TABLE interactions (
    id          SERIAL PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    media_id    INTEGER NOT NULL REFERENCES media_index(media_id),
    media_type  TEXT NOT NULL CHECK (media_type IN ('movie', 'season')),
    kind        TEXT NOT NULL CHECK (kind IN ('logged', 'rated', 'dismissed', 'saved', 'impression', 'clicked')),
    rating      SMALLINT CHECK (rating IS NULL OR (rating >= 0 AND rating <= 100)),
    source      TEXT CHECK (source IS NULL OR source IN ('search', 'detail', 'feed', 'onboarding')),
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX interactions_user_created_at_idx
    ON interactions (user_id, created_at DESC);

CREATE INDEX interactions_media_id_idx
    ON interactions (media_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS interactions;
-- +goose StatementEnd
