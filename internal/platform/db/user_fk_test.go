package db

import (
	"context"
	"testing"
)

// Every row that belongs to a user goes when the user goes, and none can exist
// without one. Account deletion leans on this, and a table added later has to
// follow it too.
func TestEveryUserReferenceIsRequiredAndCascades(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	conn, err := New(loadDBConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var refs []struct {
		Table    string `db:"tbl"`
		Column   string `db:"attname"`
		NotNull  bool   `db:"attnotnull"`
		OnDelete string `db:"confdeltype"`
	}
	err = conn.SelectContext(context.Background(), &refs, `
		select c.conrelid::regclass::text as tbl, a.attname, a.attnotnull,
		       c.confdeltype::text as confdeltype
		from pg_constraint c
		join pg_attribute a on a.attrelid = c.conrelid and a.attnum = any (c.conkey)
		where c.contype = 'f' and c.confrelid = 'public.users'::regclass
		order by 1, 2
	`)
	if err != nil {
		t.Fatalf("list user references: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("no references to users found, so the check proved nothing")
	}
	for _, r := range refs {
		if !r.NotNull {
			t.Errorf("%s.%s can be null", r.Table, r.Column)
		}
		if r.OnDelete != "c" {
			t.Errorf("%s.%s does not cascade on delete (confdeltype %q)", r.Table, r.Column, r.OnDelete)
		}
	}
}
