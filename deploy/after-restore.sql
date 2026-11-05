-- Run once after restoring a backup into a new Supabase project. README.md has the
-- steps. It is safe to run again.
--
-- A new project opens every new table in public to anon and authenticated, and a dump
-- can't take that back, since it only records what the old project granted. goose
-- won't either, since the restored goose_db_version says 00021 and 00034 already ran.
-- This repeats what those two migrations did to anything outside the dump.

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM anon, authenticated;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM anon, authenticated;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM anon, authenticated;

ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    REVOKE ALL ON TABLES FROM anon, authenticated;
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    REVOKE ALL ON SEQUENCES FROM anon, authenticated;
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    REVOKE ALL ON FUNCTIONS FROM anon, authenticated;

-- Roles aren't in the dump, since they belong to the whole server.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'backup_job') THEN
        CREATE ROLE backup_job NOLOGIN BYPASSRLS;
    END IF;
END;
$$;

COMMENT ON ROLE backup_job IS 'The nightly backup. Reads everything and writes nothing.';

GRANT pg_read_all_data TO backup_job;
