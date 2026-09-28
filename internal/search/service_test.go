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

// A search hit is cached without keywords or credits. The first detail view of
// that movie must fetch the full record and upgrade the row, or the movie stays
// invisible to the recommender forever.
func TestSearch_WarmedMovieGainsDetailOnFirstView(t *testing.T) {
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
	dbConn, err := db.New(*config.NewDB())
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	defer dbConn.Close()

	tmdbClient := tmdb.NewClient(config.NewTMDB().APIKey)
	repo := movies.NewRepository(dbConn)
	searchSvc := search.NewService(tmdbClient, repo)
	movieSvc := movies.NewService(tmdbClient, repo)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const matrixReloaded = 604
	// Only the cache row goes; media_index ids are referenced by user data.
	if _, err := dbConn.ExecContext(ctx, `delete from movies where movie_id = $1`, matrixReloaded); err != nil {
		t.Fatalf("clear cached row: %v", err)
	}

	if _, err := searchSvc.GetSearchResult(ctx, "movie", "the matrix reloaded"); err != nil {
		t.Fatalf("search: %v", err)
	}
	searchSvc.Wait()

	var warmed struct {
		Keywords []byte `db:"keywords"`
		Credits  []byte `db:"credits"`
	}
	if err := dbConn.GetContext(ctx, &warmed,
		`select keywords, credits from movies where movie_id = $1`, matrixReloaded); err != nil {
		t.Fatalf("search did not warm movie %d: %v", matrixReloaded, err)
	}
	if warmed.Credits != nil {
		t.Fatalf("precondition: a search-warmed row should have no credits, got %d bytes", len(warmed.Credits))
	}

	movie, err := movieSvc.GetMovieById(ctx, matrixReloaded, "tmdb")
	if err != nil {
		t.Fatalf("GetMovieById: %v", err)
	}
	if movie.Keywords == nil || movie.Credits == nil {
		t.Fatalf("detail view returned the skinny row: keywords=%v credits=%v", movie.Keywords != nil, movie.Credits != nil)
	}

	var upgraded struct {
		Keywords []byte `db:"keywords"`
		Credits  []byte `db:"credits"`
	}
	if err := dbConn.GetContext(ctx, &upgraded,
		`select keywords, credits from movies where movie_id = $1`, matrixReloaded); err != nil {
		t.Fatalf("read upgraded row: %v", err)
	}
	if upgraded.Keywords == nil || upgraded.Credits == nil {
		t.Errorf("row not upgraded: keywords=%d bytes credits=%d bytes", len(upgraded.Keywords), len(upgraded.Credits))
	}
}

// Verifies the slice-6 promise: a movie-type search warms the movies cache
// with the returned hits, and a repeat search is idempotent (no error, no
// duplicate rows, because the insert is ON CONFLICT).
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
	// Warming runs after the response, so wait for it before counting.
	if _, err := svc.GetSearchResult(ctx, "movie", q); err != nil {
		t.Fatalf("second search: %v", err)
	}
	svc.Wait()

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
	svc.Wait()

	var afterRepeat int
	if err := dbConn.QueryRowContext(ctx,
		`select count(*) from movies where movie_id = any($1)`, ids).Scan(&afterRepeat); err != nil {
		t.Fatalf("count movies after repeat: %v", err)
	}
	if afterRepeat != len(ids) {
		t.Errorf("repeat search produced duplicates: expected %d rows, got %d", len(ids), afterRepeat)
	}
}
