package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A new Supabase project opens every new table in public to anon and authenticated,
// and a dump can't take that back, since it only records what the old project granted.
// goose won't either, since the restored goose_db_version says 00021 and 00034 ran.
// The whole test runs in a transaction that is rolled back.
func TestAfterRestoreClosesPublicRightsAndRecreatesBackupRole(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	conn, err := New(loadDBConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()

	var version int64
	if err := conn.GetContext(ctx, &version, `select coalesce(max(version_id), 0) from goose_db_version`); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version < 34 {
		t.Skipf("migration 00034 not applied (at %d)", version)
	}

	script, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "after-restore.sql"))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	// What a restore into a new project leaves behind.
	_, err = tx.ExecContext(ctx, `
		GRANT ALL ON ALL TABLES IN SCHEMA public TO anon, authenticated;
		GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO anon, authenticated;
		GRANT ALL ON ALL FUNCTIONS IN SCHEMA public TO anon, authenticated;
		ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public GRANT ALL ON TABLES TO anon, authenticated;
		ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public GRANT ALL ON SEQUENCES TO anon, authenticated;
		ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public GRANT ALL ON FUNCTIONS TO anon, authenticated;
		REVOKE pg_read_all_data FROM backup_job;
		DROP ROLE backup_job;
	`)
	if err != nil {
		t.Fatalf("set up the restored state: %v", err)
	}

	const openTables = `
		select count(*)
		from pg_class c
		cross join (values ('anon'), ('authenticated')) as r(role)
		where c.relnamespace = 'public'::regnamespace
		  and c.relkind = 'r'
		  and has_table_privilege(r.role, c.oid, 'select, insert, update, delete, truncate')`

	var open int
	if err := tx.GetContext(ctx, &open, openTables); err != nil {
		t.Fatalf("count open tables: %v", err)
	}
	if open == 0 {
		t.Fatal("the set up opened nothing, so this test would prove nothing")
	}

	if _, err := tx.ExecContext(ctx, string(script)); err != nil {
		t.Fatalf("run script: %v", err)
	}

	if err := tx.GetContext(ctx, &open, openTables); err != nil {
		t.Fatalf("count open tables: %v", err)
	}
	if open != 0 {
		t.Errorf("anon or authenticated still reach %d table grants", open)
	}

	var openSequences int
	err = tx.GetContext(ctx, &openSequences, `
		select count(*)
		from pg_class c
		cross join (values ('anon'), ('authenticated')) as r(role)
		where c.relnamespace = 'public'::regnamespace
		  and c.relkind = 'S'
		  and has_sequence_privilege(r.role, c.oid, 'usage, select, update')`)
	if err != nil {
		t.Fatalf("count open sequences: %v", err)
	}
	if openSequences != 0 {
		t.Errorf("anon or authenticated still reach %d sequence grants", openSequences)
	}

	var grantedFunctions int
	err = tx.GetContext(ctx, &grantedFunctions, `
		select count(*)
		from pg_proc p
		where p.pronamespace = 'public'::regnamespace
		  and p.proacl::text ~ '[{,](anon|authenticated)='`)
	if err != nil {
		t.Fatalf("count granted functions: %v", err)
	}
	if grantedFunctions != 0 {
		t.Errorf("%d functions still name anon or authenticated in their grants", grantedFunctions)
	}

	// The next migration's tables have to stay closed too.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE public.after_restore_probe (id int)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	var probeOpen bool
	err = tx.GetContext(ctx, &probeOpen, `select has_table_privilege('anon', 'public.after_restore_probe', 'select')`)
	if err != nil {
		t.Fatalf("read probe rights: %v", err)
	}
	if probeOpen {
		t.Error("a table created after the script is open to anon")
	}

	var role struct {
		CanLogin  bool `db:"rolcanlogin"`
		BypassRLS bool `db:"rolbypassrls"`
		Reader    bool `db:"reader"`
	}
	err = tx.GetContext(ctx, &role, `
		select rolcanlogin, rolbypassrls, pg_has_role('backup_job', 'pg_read_all_data', 'member') as reader
		from pg_roles
		where rolname = 'backup_job'
	`)
	if err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role.CanLogin {
		t.Error("backup_job must not sign in until its password is set by hand")
	}
	if !role.BypassRLS || !role.Reader {
		t.Errorf("backup_job can't read everything: %+v", role)
	}
}
