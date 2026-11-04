package db

import (
	"context"
	"testing"
)

// The anon key ships in the web bundle, so every public table has to deny the
// PostgREST roles even if a grant slips through. A table created after the
// lockdown migration does not pick this up on its own, which is what this test
// is for. FORCE is refused too: the API connects as the table owner, and FORCE
// would apply the empty policy set to it and break every query.
func TestEveryPublicTableHasRowLevelSecurity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	conn, err := New(loadDBConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var tables []struct {
		Name   string `db:"relname"`
		RLS    bool   `db:"relrowsecurity"`
		Forced bool   `db:"relforcerowsecurity"`
	}
	err = conn.SelectContext(context.Background(), &tables, `
		select relname, relrowsecurity, relforcerowsecurity
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
		if !table.RLS {
			t.Errorf("%s: row level security is off", table.Name)
		}
		if table.Forced {
			t.Errorf("%s: FORCE row level security would lock the API out", table.Name)
		}
	}
}
