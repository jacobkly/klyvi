-- +goose Up
-- +goose StatementBegin
-- A title's family: the films and shows that share a Wikidata series (P179)
-- or media franchise (P8345) with it. franchises fills on the next open-data
-- run. The index serves the overlap lookup on the two lists together.
ALTER TABLE wikidata_facts ADD COLUMN franchises INTEGER[] NOT NULL DEFAULT '{}';
CREATE INDEX wikidata_facts_family_idx ON wikidata_facts USING gin ((series || franchises));

-- A collection lists its films in release order. TMDB's own order of parts
-- isn't always that. Old rows fill on their next refresh.
ALTER TABLE movie_collections ADD COLUMN release_date DATE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE movie_collections DROP COLUMN release_date;
DROP INDEX wikidata_facts_family_idx;
ALTER TABLE wikidata_facts DROP COLUMN franchises;
-- +goose StatementEnd
