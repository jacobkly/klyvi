package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/joeshaw/envdecode"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/platform/timing"
)

func TestQueryTracerRecordsIntoRequestRecorder(t *testing.T) {
	ctx, rec := timing.NewContext(context.Background())
	tr := queryTracer{}

	for i := 0; i < 2; i++ {
		qctx := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "select 1"})
		tr.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{})
	}

	if got := rec.Snapshot().DBQueries; got != 2 {
		t.Fatalf("DBQueries = %d, want 2", got)
	}
}

func TestQueryTracerWithoutRecorderIsNoop(t *testing.T) {
	tr := queryTracer{}
	qctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{})
	tr.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{}) // must not panic
}

// TestNewCountsQueriesPerRequest proves the tracer is actually wired into the
// pool New returns: queries made with a request context land in its Recorder.
func TestNewCountsQueriesPerRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	cfg := loadDBConfig(t)
	conn, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, rec := timing.NewContext(context.Background())
	var one int
	if err := conn.GetContext(ctx, &one, `select 1`); err != nil {
		t.Fatalf("select 1: %v", err)
	}
	// Long enough to register on Windows' coarse clock.
	if _, err := conn.ExecContext(ctx, `select pg_sleep(0.02)`); err != nil {
		t.Fatalf("exec pg_sleep: %v", err)
	}

	snap := rec.Snapshot()
	if snap.DBQueries != 2 {
		t.Fatalf("DBQueries = %d, want 2", snap.DBQueries)
	}
	if snap.DB <= 0 {
		t.Fatalf("DB duration = %v, want > 0", snap.DB)
	}
}

func loadDBConfig(t *testing.T) config.ConfDB {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env.dev")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip(".env.dev not found; skipping DB-dependent test")
		}
		dir = parent
	}
	if err := godotenv.Load(filepath.Join(dir, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}
	var c config.ConfDB
	if err := envdecode.StrictDecode(&c); err != nil {
		t.Skipf("DB env incomplete: %v", err)
	}
	return c
}
