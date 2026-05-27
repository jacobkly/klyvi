package search_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/search"

	"github.com/joho/godotenv"
)

// findRepoRoot walks up from the test's cwd until it finds .env.dev. Same
// helper as the movies package; duplicated here to keep the test packages
// independent.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env.dev")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip(".env.dev not found; skipping DB-dependent test")
		}
		dir = parent
	}
}

// Verifies the slice-6 promise: a movie-type search warms the movies cache
// with the returned hits, and a repeat search is idempotent (no error, no
// duplicate rows because InsertMovie now has ON CONFLICT DO NOTHING).
func TestSearch_MovieType_WarmsMoviesCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TMDB+DB integration test in short mode")
	}

	root := findRepoRoot(t)
	if err := godotenv.Load(filepath.Join(root, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}
	if os.Getenv("TMDB_API_KEY") == "" {
		t.Skip("TMDB_API_KEY not set")
	}

	dbCfg := config.NewDB()
	dbConn, err := db.New(*dbCfg)
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	defer dbConn.Close()

	tmdbCfg := config.NewTMDB()
	tmdbClient := tmdb.NewClient(tmdbCfg.APIKey)
	repo := movies.NewRepository(dbConn)
	svc := search.NewService(tmdbClient, repo)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const q = "the matrix reloaded"

	// First search: get the list of movie ids the search returns so we know
	// what to clear and what to assert on.
	first, err := svc.GetSearchResult(ctx, "movie", q)
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	resultMap, ok := first.(map[string]interface{})
	if !ok {
		t.Fatalf("search result not a map: %T", first)
	}
	results, ok := resultMap["results"].([]interface{})
	if !ok || len(results) == 0 {
		t.Fatalf("no results for %q (TMDB unreachable or query stale?)", q)
	}

	var ids []int
	for _, r := range results {
		item, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := item["id"].(float64); ok {
			ids = append(ids, int(id))
		}
	}
	if len(ids) == 0 {
		t.Fatalf("no ids parsed from search results")
	}

	// Reset state so the next search exercises the upsert path on fresh rows.
	cleanup := func() {
		for _, id := range ids {
			_, _ = dbConn.ExecContext(ctx, `delete from movies where movie_id = $1`, id)
			_, _ = dbConn.ExecContext(ctx,
				`delete from media_index where id = $1 and media_type = 'movie'`,
				id)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// Second search: same query, but the cache is now empty for these ids.
	if _, err := svc.GetSearchResult(ctx, "movie", q); err != nil {
		t.Fatalf("second search: %v", err)
	}

	// Count of cached rows for the searched ids must equal len(ids).
	var inserted int
	if err := dbConn.QueryRowContext(ctx,
		`select count(*) from movies where movie_id = any($1)`, ids).Scan(&inserted); err != nil {
		t.Fatalf("count movies: %v", err)
	}
	if inserted != len(ids) {
		t.Errorf("expected %d cached movie rows after search, got %d", len(ids), inserted)
	}

	// Same for media_index.
	var indexed int
	if err := dbConn.QueryRowContext(ctx,
		`select count(*) from media_index where id = any($1) and media_type = 'movie'`, ids).Scan(&indexed); err != nil {
		t.Fatalf("count media_index: %v", err)
	}
	if indexed != len(ids) {
		t.Errorf("expected %d media_index rows after search, got %d", len(ids), indexed)
	}

	// Third search: must not error and must not produce duplicates.
	if _, err := svc.GetSearchResult(ctx, "movie", q); err != nil {
		t.Fatalf("third (idempotent) search: %v", err)
	}

	var afterRepeat int
	if err := dbConn.QueryRowContext(ctx,
		`select count(*) from movies where movie_id = any($1)`, ids).Scan(&afterRepeat); err != nil {
		t.Fatalf("count movies after repeat: %v", err)
	}
	if afterRepeat != len(ids) {
		t.Errorf("repeat search produced duplicates: expected %d rows, got %d", len(ids), afterRepeat)
	}
}
