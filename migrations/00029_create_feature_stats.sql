-- +goose Up
-- +goose StatementBegin
-- feature_stats holds per-feature document frequencies over the catalog: how
-- many cached media items of a given type carry each genre / keyword id. The
-- recommender turns these counts into inverse-document-frequency weights so
-- ubiquitous features (e.g. genre Drama, "based on a true story") stop
-- dominating a large, diverse taste and collapsing its feed to the centroid,
-- while discriminative niche features surface.
--
-- Partitioned by media_type so TV feature stats slot in under the same axis
-- media_index already uses (movie | tv) with no schema change — the movies
-- recommender ships first, TV reco is later work.
CREATE TABLE feature_stats (
    media_type   TEXT        NOT NULL, -- 'movie' | 'tv' (mirrors media_index.media_type)
    feature_type TEXT        NOT NULL, -- 'genre' | 'keyword'
    feature_id   INT         NOT NULL,
    doc_count    INT         NOT NULL, -- # of docs of this media_type carrying the feature
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (media_type, feature_type, feature_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- feature_corpus holds the denominator N — the total document count per
-- media_type — so idf = log((1 + N) / (1 + doc_count)) is reproducible from
-- these two tables alone, with the exact formula living in the recommender.
CREATE TABLE feature_corpus (
    media_type TEXT        PRIMARY KEY, -- 'movie' | 'tv'
    doc_count  INT         NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS feature_corpus;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS feature_stats;
-- +goose StatementEnd
