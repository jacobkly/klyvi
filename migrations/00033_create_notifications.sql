-- +goose Up
-- +goose StatementBegin
-- A notice is one user being told about one event: a season premiering or a
-- film coming out. The unique key is what makes the nightly job safe to re-run.
-- A film has no season, so the key has to treat NULLs as equal.
CREATE TABLE notifications (
    id            BIGSERIAL PRIMARY KEY,
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('season_premiere', 'film_release')),
    media_type    TEXT NOT NULL CHECK (media_type IN ('tv', 'movie')),
    tmdb_id       INTEGER NOT NULL,
    season_number INTEGER,
    title         TEXT NOT NULL,
    event_date    DATE NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    read_at       TIMESTAMPTZ,
    CONSTRAINT notifications_once
        UNIQUE NULLS NOT DISTINCT (user_id, kind, media_type, tmdb_id, season_number),
    CONSTRAINT notifications_kind_shape CHECK (
        (kind = 'season_premiere' AND media_type = 'tv' AND season_number IS NOT NULL)
        OR (kind = 'film_release' AND media_type = 'movie' AND season_number IS NULL)
    )
);

CREATE INDEX notifications_user_created_idx ON notifications (user_id, created_at DESC, id DESC);

-- Same deny-by-default posture as every other table: RLS on with no policies.
-- Never add FORCE, see 00021.
ALTER TABLE public.notifications ENABLE ROW LEVEL SECURITY;

ALTER TABLE users
    ADD COLUMN notify_premieres BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN notify_releases  BOOLEAN NOT NULL DEFAULT TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users
    DROP COLUMN notify_releases,
    DROP COLUMN notify_premieres;

DROP TABLE notifications;
-- +goose StatementEnd
