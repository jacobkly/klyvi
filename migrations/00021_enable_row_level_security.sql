-- +goose Up
-- +goose StatementBegin
-- Lock the database down to the Go API only.
--
-- Supabase ships the `public` schema with full DELETE/INSERT/SELECT/TRUNCATE/
-- UPDATE grants to the `anon` and `authenticated` roles, and RLS off. Those
-- are the roles PostgREST assumes when a caller presents the project's anon
-- key -- a key that ships inside the frontend bundle and is public by design.
-- Left as-is, anyone who reads that key can select every user's tracking
-- history, rewrite taste profiles, or truncate `users`.
--
-- Klyvi's clients never talk to Postgres directly. Catalog, tracking,
-- interactions, and recommendations all go through this API, which
-- authorizes every request from the verified Supabase JWT and scopes its
-- queries by the user UUID it carries. So the correct posture for the
-- PostgREST roles is no access at all, not per-row policies.
--
-- Two mechanisms, because either alone is a single point of failure:
--   1. RLS enabled with zero policies -> every row is invisible to any role
--      that is not the table owner. Deny-by-default.
--   2. Privileges revoked -> those roles cannot reach the table to begin with.
--
-- The API is unaffected: it connects as `postgres`, which owns these tables,
-- and a table owner bypasses RLS. Do NOT add FORCE ROW LEVEL SECURITY to
-- these tables -- that is the one setting that would apply RLS to the owner
-- and break every query the API makes.
DO $$
DECLARE
    t TEXT;
BEGIN
    FOR t IN
        SELECT tablename FROM pg_tables WHERE schemaname = 'public'
    LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', t);
    END LOOP;
END $$;

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM anon, authenticated;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM anon, authenticated;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM anon, authenticated;

-- Without this, the next migration that creates a table hands anon full
-- access again and this fix silently regresses.
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    REVOKE ALL ON TABLES FROM anon, authenticated;
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    REVOKE ALL ON SEQUENCES FROM anon, authenticated;
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    REVOKE ALL ON FUNCTIONS FROM anon, authenticated;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restores Supabase's default open-to-PostgREST posture. Running this
-- reopens direct anon-key access to every table; only do so if you are
-- deliberately moving read paths back onto supabase-js.
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    GRANT ALL ON TABLES TO anon, authenticated;
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    GRANT ALL ON SEQUENCES TO anon, authenticated;
ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
    GRANT ALL ON FUNCTIONS TO anon, authenticated;

GRANT ALL ON ALL TABLES IN SCHEMA public TO anon, authenticated;
GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO anon, authenticated;
GRANT ALL ON ALL FUNCTIONS IN SCHEMA public TO anon, authenticated;

DO $$
DECLARE
    t TEXT;
BEGIN
    FOR t IN
        SELECT tablename FROM pg_tables WHERE schemaname = 'public'
    LOOP
        EXECUTE format('ALTER TABLE public.%I DISABLE ROW LEVEL SECURITY', t);
    END LOOP;
END $$;
-- +goose StatementEnd
