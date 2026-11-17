-- +goose Up
-- +goose StatementBegin
-- pgvector, in the extensions schema like Supabase's other extensions.
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA extensions;

-- wikipedia_plots holds one vector per catalog title, embedded from the plot
-- section of the title's English Wikipedia article (CC BY-SA), found through
-- wikidata_facts.wikipedia_url. Only Wikipedia text is ever embedded, never
-- TMDB's. revision_id is the article revision the row was built from, so the
-- weekly job re-embeds only articles that changed. embedding is null when that
-- revision has no usable plot section, which still saves refetching it.
CREATE TABLE wikipedia_plots (
    media_type  TEXT        NOT NULL CHECK (media_type IN ('movie', 'tv')),
    tmdb_id     INTEGER     NOT NULL,
    revision_id BIGINT      NOT NULL,
    model       TEXT        NOT NULL,
    plot_words  INTEGER     NOT NULL DEFAULT 0,
    embedding   extensions.halfvec(384),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_type, tmdb_id)
);

-- Same deny-by-default posture as every other table: RLS on with no policies.
-- Never add FORCE, see 00021.
ALTER TABLE public.wikipedia_plots ENABLE ROW LEVEL SECURITY;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE wikipedia_plots;
-- The extension stays: dropping it could break anything else that uses it.
-- +goose StatementEnd
