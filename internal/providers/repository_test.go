package providers_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/providers"
)

// A synthetic id far above any real one.
const testID = 2_100_003_000

func setupDB(t *testing.T) *sqlx.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
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
	dbConn, err := db.New(*config.NewDB())
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	t.Cleanup(func() { _ = dbConn.Close() })

	var version int64
	if err := dbConn.Get(&version, `select coalesce(max(version_id), 0) from goose_db_version where is_applied`); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if version < 42 {
		t.Skipf("migration 00042 not applied (DB is at %d)", version)
	}
	clean := func() {
		_, _ = dbConn.Exec(`delete from watch_providers where tmdb_id = $1`, testID)
	}
	clean()
	t.Cleanup(clean)
	return dbConn
}

func TestRepository_PutGetAndPrune(t *testing.T) {
	dbh := setupDB(t)
	repo := providers.NewRepository(dbh)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if got, err := repo.Get(ctx, "tv", testID); err != nil || got != nil {
		t.Fatalf("empty Get: %v %v, want nil nil", got, err)
	}
	results := json.RawMessage(`{"US":{"flatrate":[{"provider_id":350,"provider_name":"Apple TV+"}]}}`)
	if err := repo.Put(ctx, "tv", testID, results); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// A second put replaces the first.
	if err := repo.Put(ctx, "tv", testID, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Put again: %v", err)
	}
	got, err := repo.Get(ctx, "tv", testID)
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if string(got.Results) != "{}" || time.Since(got.FetchedAt) > time.Minute {
		t.Errorf("got %s fetched %s", got.Results, got.FetchedAt)
	}
	if other, _ := repo.Get(ctx, "movie", testID); other != nil {
		t.Errorf("the movie with the same id shares the series' copy")
	}

	if _, err := dbh.ExecContext(ctx, `update watch_providers set fetched_at = now() - interval '200 days' where tmdb_id = $1`, testID); err != nil {
		t.Fatalf("age the row: %v", err)
	}
	n, err := repo.DeleteOlderThan(ctx, time.Now().Add(-150*24*time.Hour))
	if err != nil || n < 1 {
		t.Fatalf("DeleteOlderThan: %d %v", n, err)
	}
	if got, _ := repo.Get(ctx, "tv", testID); got != nil {
		t.Errorf("an old copy survived the prune")
	}
}
