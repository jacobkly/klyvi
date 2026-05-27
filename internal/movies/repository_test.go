package movies_test

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"

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
