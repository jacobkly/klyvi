-- +goose Up
-- +goose StatementBegin
CREATE TABLE imports (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source      TEXT NOT NULL CHECK (source IN ('letterboxd', 'trakt', 'csv')),
    status      TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'running', 'done', 'failed')),
    total       INT NOT NULL DEFAULT 0,
    matched     INT NOT NULL DEFAULT 0,
    unmatched   INT NOT NULL DEFAULT 0,
    error       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX imports_user_created_idx ON imports (user_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS imports;
-- +goose StatementEnd
