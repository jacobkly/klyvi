-- +goose Up
-- +goose StatementBegin
-- wikidata_facts holds open facts about catalog titles from Wikidata (CC0),
-- matched by the TMDB id Wikidata records for them. The recommender may model
-- these, unlike TMDB's own text. Every list holds Wikidata item numbers (Q42 is
-- 42), sorted and without repeats. A title Wikidata has no match for has no row.
-- wikipedia_url is the English Wikipedia article, the source for plot text.
CREATE TABLE wikidata_facts (
    media_type    TEXT      NOT NULL CHECK (media_type IN ('movie', 'tv')),
    tmdb_id       INTEGER   NOT NULL,
    wikidata_id   INTEGER   NOT NULL,
    wikipedia_url TEXT,
    genres        INTEGER[] NOT NULL DEFAULT '{}',
    cast_members  INTEGER[] NOT NULL DEFAULT '{}',
    directors     INTEGER[] NOT NULL DEFAULT '{}',
    subjects      INTEGER[] NOT NULL DEFAULT '{}',
    locations     INTEGER[] NOT NULL DEFAULT '{}',
    based_on      INTEGER[] NOT NULL DEFAULT '{}',
    countries     INTEGER[] NOT NULL DEFAULT '{}',
    series        INTEGER[] NOT NULL DEFAULT '{}',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_type, tmdb_id)
);

-- Same deny-by-default posture as every other table: RLS on with no policies.
-- Never add FORCE, see 00021.
ALTER TABLE public.wikidata_facts ENABLE ROW LEVEL SECURITY;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE wikidata_facts;
-- +goose StatementEnd
