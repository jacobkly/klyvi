-- +goose Up
-- +goose StatementBegin
-- A policy notice tells every account that the terms or privacy policy are
-- changing. It is about no title, so the media columns go nullable and the
-- shape check holds each kind to its own columns. It points at a page on the
-- site through link.
--
-- The one-per-event key moves to partial unique indexes: the old key for the
-- title notices, and the notice text for policy ones, so posting the same
-- change twice tells nobody twice. ON CONFLICT DO NOTHING with no target still
-- covers both.
ALTER TABLE notifications
    DROP CONSTRAINT notifications_once,
    DROP CONSTRAINT notifications_kind_shape,
    DROP CONSTRAINT notifications_kind_check,
    ALTER COLUMN media_type DROP NOT NULL,
    ALTER COLUMN tmdb_id DROP NOT NULL,
    ADD COLUMN link TEXT;

ALTER TABLE notifications
    ADD CONSTRAINT notifications_kind_check
        CHECK (kind IN ('season_premiere', 'film_release', 'policy')),
    ADD CONSTRAINT notifications_kind_shape CHECK (
        (kind = 'season_premiere' AND media_type = 'tv' AND tmdb_id IS NOT NULL
            AND season_number IS NOT NULL AND link IS NULL)
        OR (kind = 'film_release' AND media_type = 'movie' AND tmdb_id IS NOT NULL
            AND season_number IS NULL AND link IS NULL)
        OR (kind = 'policy' AND media_type IS NULL AND tmdb_id IS NULL
            AND season_number IS NULL AND link IS NOT NULL)
    );

CREATE UNIQUE INDEX notifications_once
    ON notifications (user_id, kind, media_type, tmdb_id, season_number) NULLS NOT DISTINCT
    WHERE kind <> 'policy';

CREATE UNIQUE INDEX notifications_policy_once
    ON notifications (user_id, title)
    WHERE kind = 'policy';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM notifications WHERE kind = 'policy';

DROP INDEX notifications_policy_once;
DROP INDEX notifications_once;

ALTER TABLE notifications
    DROP CONSTRAINT notifications_kind_shape,
    DROP CONSTRAINT notifications_kind_check,
    DROP COLUMN link,
    ALTER COLUMN media_type SET NOT NULL,
    ALTER COLUMN tmdb_id SET NOT NULL;

ALTER TABLE notifications
    ADD CONSTRAINT notifications_kind_check
        CHECK (kind IN ('season_premiere', 'film_release')),
    ADD CONSTRAINT notifications_once
        UNIQUE NULLS NOT DISTINCT (user_id, kind, media_type, tmdb_id, season_number),
    ADD CONSTRAINT notifications_kind_shape CHECK (
        (kind = 'season_premiere' AND media_type = 'tv' AND season_number IS NOT NULL)
        OR (kind = 'film_release' AND media_type = 'movie' AND season_number IS NULL)
    );
-- +goose StatementEnd
