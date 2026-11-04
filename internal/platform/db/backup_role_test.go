package db

import (
	"context"
	"testing"
)

// The backup role signs in from a script on the home server, so a leaked password
// should cost a reader, never a writer. It also has to read every table, or the
// nightly dump quietly stops being restorable, so a table added later is checked
// here too. The privileges are asked about rather than tried, since hosted Supabase
// doesn't let the postgres role SET ROLE to everything.
func TestBackupRoleReadsEverythingAndWritesNothing(t *testing.T) {
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

	var role struct {
		BypassRLS  bool `db:"rolbypassrls"`
		Super      bool `db:"rolsuper"`
		CreateRole bool `db:"rolcreaterole"`
		CreateDB   bool `db:"rolcreatedb"`
	}
	err = conn.GetContext(ctx, &role, `
		select rolbypassrls, rolsuper, rolcreaterole, rolcreatedb
		from pg_roles
		where rolname = 'backup_job'
	`)
	if err != nil {
		t.Fatalf("read role: %v", err)
	}
	if !role.BypassRLS {
		t.Error("backup_job must bypass row level security, or pg_dump refuses every public table")
	}
	if role.Super || role.CreateRole || role.CreateDB {
		t.Errorf("backup_job holds too much: %+v", role)
	}

	var tables []struct {
		Name     string `db:"relname"`
		Select   bool   `db:"can_select"`
		Insert   bool   `db:"can_insert"`
		Update   bool   `db:"can_update"`
		Delete   bool   `db:"can_delete"`
		Truncate bool   `db:"can_truncate"`
	}
	err = conn.SelectContext(ctx, &tables, `
		select relname,
		       has_table_privilege('backup_job', oid, 'SELECT')   as can_select,
		       has_table_privilege('backup_job', oid, 'INSERT')   as can_insert,
		       has_table_privilege('backup_job', oid, 'UPDATE')   as can_update,
		       has_table_privilege('backup_job', oid, 'DELETE')   as can_delete,
		       has_table_privilege('backup_job', oid, 'TRUNCATE') as can_truncate
		from pg_class
		where relnamespace = 'public'::regnamespace and relkind in ('r', 'p')
		order by relname
	`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no public tables found, so the check proved nothing")
	}
	for _, table := range tables {
		if !table.Select {
			t.Errorf("%s: backup_job can't read it, so the dump would fail", table.Name)
		}
		if table.Insert || table.Update || table.Delete || table.Truncate {
			t.Errorf("%s: backup_job can write to it", table.Name)
		}
	}
}
