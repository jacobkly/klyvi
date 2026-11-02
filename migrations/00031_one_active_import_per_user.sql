-- +goose Up
-- +goose StatementBegin
-- A user may have one import pending or running at a time, so a second upload
-- cannot pile onto the first. Jobs an old restart left behind may already
-- break that, so each user's newest active job is kept and the rest are
-- failed before the index is built.
UPDATE imports AS i
SET status = 'failed',
    error = 'import did not complete (worker stopped)',
    updated_at = NOW()
WHERE i.status IN ('pending', 'running')
  AND EXISTS (
      SELECT 1 FROM imports AS newer
      WHERE newer.user_id = i.user_id
        AND newer.status IN ('pending', 'running')
        AND (newer.created_at, newer.id) > (i.created_at, i.id)
  );

CREATE UNIQUE INDEX imports_one_active_per_user
    ON imports (user_id)
    WHERE status IN ('pending', 'running');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS imports_one_active_per_user;
-- +goose StatementEnd
