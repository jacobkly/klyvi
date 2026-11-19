package errlog_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/errlog"
	"klyvi-api/internal/platform/db"
)

// openTx returns a transaction that is rolled back when the test ends, so
// nothing a test records outlives it. If migration 00043 isn't applied yet,
// the transaction creates the table from the migration itself, which tests
// the migration too, and the rollback takes it away again.
func openTx(t *testing.T) *sqlx.Tx {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, ".env.dev")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Skip(".env.dev not found; skipping DB-dependent test")
		}
		root = parent
	}
	if err := godotenv.Load(filepath.Join(root, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}
	conn, err := db.New(*config.NewDB())
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	tx, err := conn.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	var exists bool
	if err := tx.Get(&exists, `select to_regclass('public.error_events') is not null`); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if !exists {
		raw, err := os.ReadFile(filepath.Join(root, "migrations", "00043_create_error_events.sql"))
		if err != nil {
			t.Fatalf("read migration: %v", err)
		}
		up := strings.Split(string(raw), "-- +goose Down")[0]
		if _, err := tx.Exec(up); err != nil {
			t.Fatalf("apply migration in the test transaction: %v", err)
		}
	}
	return tx
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

type row struct {
	Count      int        `db:"count"`
	Message    string     `db:"message"`
	ResolvedAt *time.Time `db:"resolved_at"`
}

func get(t *testing.T, tx *sqlx.Tx, fingerprint string) *row {
	t.Helper()
	var rows []row
	if err := tx.Select(&rows, `select count, message, resolved_at from error_events where fingerprint = $1`, fingerprint); err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

func TestStore_TheSamePanicTwiceIsOneRowWithCount2(t *testing.T) {
	tx := openTx(t)
	store := errlog.NewStore(tx)
	e := errlog.Event{Source: errlog.SourceAPI, Kind: "panic", Message: "boom", Anchor: "klyvi-api/internal/test.boom", Route: "/v1/x"}

	for i := 0; i < 2; i++ {
		if ok, err := store.Record(ctx(t), e); err != nil || !ok {
			t.Fatalf("Record %d: %v %v", i, ok, err)
		}
	}
	got := get(t, tx, e.Fingerprint())
	if got == nil || got.Count != 2 {
		t.Fatalf("row = %+v, want one row with count 2", got)
	}
}

func TestStore_ATokenIsStoredRedacted(t *testing.T) {
	tx := openTx(t)
	store := errlog.NewStore(tx)
	e := errlog.Event{
		Source:  errlog.SourceWeb,
		Kind:    "Error",
		Message: "request with Bearer eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln for a@b.co failed",
		Route:   "/library",
	}
	if _, err := store.Record(ctx(t), e); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var messages []string
	if err := tx.Select(&messages, `select message from error_events where route = '/library' and source = 'web'`); err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(messages) != 1 || messages[0] != "request with Bearer [redacted] for [redacted] failed" {
		t.Fatalf("messages = %q", messages)
	}
}

func TestStore_The1001stWebGroupIsDropped(t *testing.T) {
	tx := openTx(t)
	store := errlog.NewStore(tx)

	// A group that exists before the cap fills keeps counting after.
	known := errlog.Event{Source: errlog.SourceWeb, Kind: "Error", Message: "known", Route: "/known"}
	if ok, err := store.Record(ctx(t), known); err != nil || !ok {
		t.Fatalf("Record known: %v %v", ok, err)
	}

	// Fill the web groups to the cap in one statement.
	if _, err := tx.Exec(`
		insert into error_events (fingerprint, source, kind, message)
		select 'test-filler-' || i, 'web', 'Error', 'filler'
		from generate_series(1, $1::int) as i
		on conflict (fingerprint) do nothing`, errlog.DefaultMaxWebGroups); err != nil {
		t.Fatalf("fill: %v", err)
	}

	web := errlog.Event{Source: errlog.SourceWeb, Kind: "Error", Message: "new", Route: "/one-too-many"}
	if ok, err := store.Record(ctx(t), web); err != nil || ok {
		t.Fatalf("the 1,001st group: stored=%v err=%v, want dropped", ok, err)
	}
	if get(t, tx, web.Fingerprint()) != nil {
		t.Fatal("the 1,001st group is in the table")
	}

	if ok, err := store.Record(ctx(t), known); err != nil || !ok {
		t.Fatalf("a known group past the cap: stored=%v err=%v", ok, err)
	}
	if got := get(t, tx, known.Fingerprint()); got == nil || got.Count != 2 {
		t.Fatalf("known group = %+v, want count 2", got)
	}

	// The API's own errors aren't capped by web noise.
	api := errlog.Event{Source: errlog.SourceAPI, Kind: "500", Message: "failed", Route: "/v1/feed", Method: "GET", Status: 500}
	if ok, err := store.Record(ctx(t), api); err != nil || !ok {
		t.Fatalf("an API error past the web cap: stored=%v err=%v", ok, err)
	}
}

func TestStore_AResolvedErrorThatRecursReopens(t *testing.T) {
	tx := openTx(t)
	store := errlog.NewStore(tx)
	e := errlog.Event{Source: errlog.SourceAPI, Kind: "500", Message: "failed", Route: "/v1/reopen", Method: "GET", Status: 500}
	if _, err := store.Record(ctx(t), e); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`update error_events set resolved_at = now() where fingerprint = $1`, e.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(ctx(t), e); err != nil {
		t.Fatal(err)
	}
	if got := get(t, tx, e.Fingerprint()); got == nil || got.ResolvedAt != nil || got.Count != 2 {
		t.Fatalf("row = %+v, want open again with count 2", got)
	}
}

func TestStore_PruneDropsGroupsUnseenFor30Days(t *testing.T) {
	tx := openTx(t)
	store := errlog.NewStore(tx)
	old := errlog.Event{Source: errlog.SourceAPI, Kind: "500", Message: "old", Route: "/v1/old", Method: "GET", Status: 500}
	fresh := errlog.Event{Source: errlog.SourceAPI, Kind: "500", Message: "fresh", Route: "/v1/fresh", Method: "GET", Status: 500}
	for _, e := range []errlog.Event{old, fresh} {
		if _, err := store.Record(ctx(t), e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`update error_events set last_seen = now() - interval '31 days' where fingerprint = $1`, old.Fingerprint()); err != nil {
		t.Fatal(err)
	}

	n, err := store.Prune(ctx(t), errlog.Retention)
	if err != nil || n < 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if get(t, tx, old.Fingerprint()) != nil {
		t.Error("a group unseen for 31 days survived")
	}
	if get(t, tx, fresh.Fingerprint()) == nil {
		t.Error("a fresh group was pruned")
	}
}
