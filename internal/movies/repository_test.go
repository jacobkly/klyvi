package movies_test

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"

	"github.com/joho/godotenv"
)

// findRepoRoot walks up from the test's cwd until it finds .env.dev, so the
// test can be run from anywhere inside the module without hardcoding paths.
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

// First test in the repo. Verifies the slice-2 fix: the media_index UNIQUE
// constraint must treat NULLs as equal so that concurrent EnsureMediaIndex
// calls for the same movie id collapse to a single row via ON CONFLICT.
// Before the fix, Postgres' default NULL ≠ NULL semantics in UNIQUE let
// duplicates through.
func TestEnsureMediaIndex_ConcurrentInsertProducesOneRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}

	root := findRepoRoot(t)
	if err := godotenv.Load(filepath.Join(root, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}

	cfg := config.NewDB()
	dbConn, err := db.New(*cfg)
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	defer dbConn.Close()

	repo := movies.NewRepository(dbConn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Pick an id well outside TMDB's plausible range so it cannot collide with
	// real cached movies. TMDB movie ids are in the low millions.
	testID := 2_000_000_000 + rand.Intn(100_000_000)

	cleanup := func() {
		_, _ = dbConn.ExecContext(ctx,
			`delete from media_index where id = $1 and media_type = 'movie'`,
			testID)
	}
	cleanup()
	t.Cleanup(cleanup)

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if err := repo.EnsureMediaIndex(ctx, testID); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("EnsureMediaIndex: %v", e)
	}

	var count int
	if err := dbConn.GetContext(ctx, &count,
		`select count(*) from media_index where id = $1 and media_type = 'movie'`,
		testID); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after concurrent EnsureMediaIndex, got %d", count)
	}
}

// Verifies the slice-4 wiring end to end: the TMDB detail call uses
// append_to_response=keywords,credits, normalize unwraps the keywords
// envelope, and the row persisted to the movies table has both columns
// populated with the right shape (keywords is the inner array, not the
// outer wrapper).
func TestService_GetMovieById_PopulatesKeywordsAndCredits(t *testing.T) {
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
	svc := movies.NewService(tmdbClient, repo)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const movieID = 550 // Fight Club — stable TMDB id with both keywords and credits.

	// Ensure we exercise the fetch+persist path, not the cache hit path.
	_, _ = dbConn.ExecContext(ctx, `delete from movies where movie_id = $1`, movieID)
	_, _ = dbConn.ExecContext(ctx,
		`delete from media_index where id = $1 and media_type = 'movie'`,
		movieID)

	movie, err := svc.GetMovieById(ctx, movieID, "tmdb")
	if err != nil {
		t.Fatalf("GetMovieById: %v", err)
	}
	if movie == nil {
		t.Fatal("expected movie, got nil")
	}
	if movie.Keywords == nil {
		t.Error("Keywords on returned movie is nil")
	}
	if movie.Credits == nil {
		t.Error("Credits on returned movie is nil")
	}

	// Verify the persisted row, not just the in-memory normalize output.
	var keywords, credits []byte
	if err := dbConn.QueryRowContext(ctx,
		`select keywords, credits from movies where movie_id = $1`,
		movieID).Scan(&keywords, &credits); err != nil {
		t.Fatalf("read persisted row: %v", err)
	}
	if len(keywords) == 0 {
		t.Fatal("persisted keywords column is empty")
	}
	if len(credits) == 0 {
		t.Fatal("persisted credits column is empty")
	}

	// keywords must be an array of {id, name} objects — not the TMDB wrapper.
	var kw []map[string]any
	if err := json.Unmarshal(keywords, &kw); err != nil {
		t.Fatalf("keywords is not a JSON array (likely still wrapped): %v\nraw: %s", err, keywords)
	}
	if len(kw) == 0 {
		t.Error("expected non-empty keywords array for movie 550")
	} else if _, ok := kw[0]["id"]; !ok {
		t.Errorf("first keyword missing id field: %+v", kw[0])
	}

	// credits should be an object containing cast/crew arrays.
	var cr map[string]any
	if err := json.Unmarshal(credits, &cr); err != nil {
		t.Fatalf("credits is not a JSON object: %v", err)
	}
	if _, ok := cr["cast"]; !ok {
		t.Error("credits.cast missing")
	}
	if _, ok := cr["crew"]; !ok {
		t.Error("credits.crew missing")
	}
}
