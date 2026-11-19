-- +goose Up
-- +goose StatementBegin
-- Errors from the API and the web client, grouped by fingerprint: the same
-- failure in the same place is one row whose count goes up. Messages are
-- scrubbed of tokens and emails before they get here. user_id is the
-- signed-in user, if any, so a report can be followed up. It has no foreign
-- key, so an error row never blocks or rides along with an account delete;
-- the delete nulls it instead. Rows unseen for 30 days are pruned nightly.
CREATE TABLE error_events (
    id BIGSERIAL PRIMARY KEY,
    fingerprint TEXT NOT NULL UNIQUE,
    source TEXT NOT NULL CHECK (source IN ('api', 'web')),
    kind TEXT NOT NULL,
    message TEXT NOT NULL,
    stack TEXT,
    route TEXT,
    method TEXT,
    status INTEGER,
    user_id UUID,
    count INTEGER NOT NULL DEFAULT 1,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ
);

CREATE INDEX error_events_last_seen_idx ON error_events (last_seen);
CREATE INDEX error_events_user_id_idx ON error_events (user_id) WHERE user_id IS NOT NULL;

ALTER TABLE public.error_events ENABLE ROW LEVEL SECURITY;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE error_events;
-- +goose StatementEnd
