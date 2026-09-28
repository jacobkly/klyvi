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

	"github.com/jmoiron/sqlx"
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

// openTestRepo connects to the dev DB, or skips when it is unavailable.
func openTestRepo(t *testing.T) (*movies.Repository, *sqlx.DB) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	root := findRepoRoot(t)
	if err := godotenv.Load(filepath.Join(root, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}
	dbConn, err := db.New(*config.NewDB())
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	t.Cleanup(func() { _ = dbConn.Close() })
	return movies.NewRepository(dbConn), dbConn
}

// cleanupTestMovies removes the synthetic rows a test wrote.
func cleanupTestMovies(t *testing.T, dbConn *sqlx.DB, ids ...int) {
	t.Helper()
	clean := func() {
		ctx := context.Background()
		_, _ = dbConn.ExecContext(ctx, `delete from movies where movie_id = any($1)`, ids)
		_, _ = dbConn.ExecContext(ctx, `delete from media_index where id = any($1) and media_type = 'movie'`, ids)
	}
	clean()
	t.Cleanup(clean)
}

func testMovie(id int, withDetail bool) *movies.Movie {
	title := "Klyvi test movie"
	m := &movies.Movie{MovieID: id, Title: &title}
	if withDetail {
		kw := json.RawMessage(`[{"id":825,"name":"support group"}]`)
		cr := json.RawMessage(`{"cast":[],"crew":[]}`)
		m.Keywords, m.Credits = &kw, &cr
	}
	return m
}

func readDetailColumns(t *testing.T, dbConn *sqlx.DB, id int) (keywords, credits []byte) {
	t.Helper()
	if err := dbConn.QueryRowContext(context.Background(),
		`select keywords, credits from movies where movie_id = $1`, id).Scan(&keywords, &credits); err != nil {
		t.Fatalf("read row %d: %v", id, err)
	}
	return keywords, credits
}

// A search hit caches a skinny row. The full detail fetch that follows must
// replace it, and a later search hit must never strip the detail back off.
func TestInsertMovie_UpgradesSkinnyRowsAndNeverDowngrades(t *testing.T) {
	repo, dbConn := openTestRepo(t)
	ctx := context.Background()
	id := 2_000_000_000 + rand.Intn(100_000_000)
	cleanupTestMovies(t, dbConn, id)

	if err := repo.InsertMovie(ctx, testMovie(id, false)); err != nil {
		t.Fatalf("insert skinny: %v", err)
	}
	if err := repo.InsertMovie(ctx, testMovie(id, true)); err != nil {
		t.Fatalf("insert rich: %v", err)
	}
	if kw, cr := readDetailColumns(t, dbConn, id); kw == nil || cr == nil {
		t.Fatalf("rich insert did not upgrade the skinny row: keywords=%s credits=%s", kw, cr)
	}

	if err := repo.InsertMovie(ctx, testMovie(id, false)); err != nil {
		t.Fatalf("insert skinny again: %v", err)
	}
	if kw, cr := readDetailColumns(t, dbConn, id); kw == nil || cr == nil {
		t.Fatalf("skinny insert downgraded the rich row: keywords=%s credits=%s", kw, cr)
	}
}

// Search warms every hit in one statement per table, tolerating duplicate ids
// and rows that already exist.
func TestInsertMovies_BatchWritesMoviesAndIndex(t *testing.T) {
	repo, dbConn := openTestRepo(t)
	ctx := context.Background()
	a := 2_000_000_000 + rand.Intn(100_000_000)
	b, c := a+1, a+2
	cleanupTestMovies(t, dbConn, a, b, c)

	if err := repo.InsertMovie(ctx, testMovie(c, true)); err != nil {
		t.Fatalf("seed existing row: %v", err)
	}

	batch := []*movies.Movie{testMovie(a, false), testMovie(b, false), testMovie(a, false), testMovie(c, false)}
	if err := repo.InsertMovies(ctx, batch); err != nil {
		t.Fatalf("InsertMovies: %v", err)
	}
	if err := repo.EnsureMediaIndexes(ctx, []int{a, b, a, c}); err != nil {
		t.Fatalf("EnsureMediaIndexes: %v", err)
	}
	if err := repo.EnsureMediaIndexes(ctx, []int{a, b, c}); err != nil {
		t.Fatalf("EnsureMediaIndexes repeat: %v", err)
	}

	var movieRows, indexRows int
	ids := []int{a, b, c}
	if err := dbConn.GetContext(ctx, &movieRows, `select count(*) from movies where movie_id = any($1)`, ids); err != nil {
		t.Fatalf("count movies: %v", err)
	}
	if err := dbConn.GetContext(ctx, &indexRows, `select count(*) from media_index where id = any($1) and media_type = 'movie'`, ids); err != nil {
		t.Fatalf("count media_index: %v", err)
	}
	if movieRows != 3 || indexRows != 3 {
		t.Errorf("rows: movies=%d media_index=%d, want 3 and 3", movieRows, indexRows)
	}
	if kw, _ := readDetailColumns(t, dbConn, c); kw == nil {
		t.Error("batch insert downgraded an existing rich row")
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
