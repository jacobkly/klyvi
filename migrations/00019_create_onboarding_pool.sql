-- +goose Up
-- +goose StatementBegin
CREATE TABLE onboarding_pool (
    tmdb_id        BIGINT PRIMARY KEY,
    dimension      TEXT NOT NULL,
    display_order  INT NOT NULL DEFAULT 0,
    active         BOOLEAN NOT NULL DEFAULT TRUE,
    added_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX onboarding_pool_active_order_idx
    ON onboarding_pool (display_order)
    WHERE active = TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS onboarding_pool;
-- +goose StatementEnd
