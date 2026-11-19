-- +goose Up
-- +goose StatementBegin
-- Where to watch a title, per country, as TMDB returns it (data by JustWatch).
-- A copy over a week old refetches when its title is viewed. The nightly
-- refresh deletes copies past the catalog age, so none outlives TMDB's
-- six-month cache limit.
CREATE TABLE watch_providers (
    media_type TEXT NOT NULL CHECK (media_type IN ('movie', 'tv')),
    tmdb_id INTEGER NOT NULL,
    results JSONB NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (media_type, tmdb_id)
);

CREATE INDEX watch_providers_fetched_at_idx ON watch_providers (fetched_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE watch_providers;
-- +goose StatementEnd
