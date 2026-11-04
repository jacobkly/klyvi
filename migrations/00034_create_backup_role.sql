-- +goose Up
-- +goose StatementBegin
-- The nightly backup's own role (deploy/backup.sh). It reads everything, past RLS,
-- since a backup that skips rows can't be restored, and pg_read_all_data grants no
-- writes. It can't sign in until its password is set outside the repo, in the SQL
-- editor, since a password can't be committed:
--
--   ALTER ROLE backup_job WITH LOGIN PASSWORD '...';
--
-- Roles belong to the whole server, so it is only created if it isn't there yet.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'backup_job') THEN
        CREATE ROLE backup_job NOLOGIN BYPASSRLS;
    END IF;
END;
$$;

COMMENT ON ROLE backup_job IS 'The nightly backup. Reads everything and writes nothing.';

GRANT pg_read_all_data TO backup_job;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
REVOKE pg_read_all_data FROM backup_job;
DROP ROLE IF EXISTS backup_job;
-- +goose StatementEnd
