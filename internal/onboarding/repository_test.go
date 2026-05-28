package onboarding_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/onboarding"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"

	"github.com/joho/godotenv"
)

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

// Verifies the pool endpoint returns enriched data: it joins onboarding_pool
// rows with cached movies.Movie data (title, poster_path, release_year)
// before responding. Films that are not yet in the cache are fetched
// transparently via the movies service.
func TestOnboarding_ListEnriched_JoinsCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB+TMDB integration test in short mode")
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
	movieRepo := movies.NewRepository(dbConn)
	movieSvc := movies.NewService(tmdbClient, movieRepo)

	repo := onboarding.NewRepository(dbConn)
	svc := onboarding.NewService(repo, movieSvc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Seed two pool rows. Use known-cached movies (Fight Club 550 + Inception
	// 27205) so we exercise the cache hit path without TMDB round trips.
	// One is marked inactive to verify the filter works.
	t.Cleanup(func() {
		_, _ = dbConn.ExecContext(ctx,
			`delete from onboarding_pool where tmdb_id in (550, 27205, 13)`)
	})
	for _, e := range []onboarding.PoolEntry{
		{TMDBID: 550, Dimension: "auteur", DisplayOrder: 1, Active: true},
		{TMDBID: 27205, Dimension: "auteur", DisplayOrder: 2, Active: true},
		{TMDBID: 13, Dimension: "classic", DisplayOrder: 99, Active: false},
	} {
		if err := repo.Upsert(ctx, e); err != nil {
			t.Fatalf("Upsert tmdb_id=%d: %v", e.TMDBID, err)
		}
	}

	// Asking for limit=2 returns just our two seeded rows because they
	// have the lowest display_order in the pool (1 and 2 — well below
	// any production seed row which starts at 10).
	got, err := svc.ListEnriched(ctx, 2)
	if err != nil {
		t.Fatalf("ListEnriched: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 active entries, got %d", len(got))
	}

	// Ordered by display_order — Fight Club (1) before Inception (2).
	if got[0].TMDBID != 550 || got[1].TMDBID != 27205 {
		t.Errorf("order wrong: got %v", []int64{got[0].TMDBID, got[1].TMDBID})
	}

	// Enrichment ran: title, poster, year present for both.
	for i, e := range got {
		if e.Title == "" {
			t.Errorf("entry %d (tmdb=%d): title empty — catalog enrichment failed", i, e.TMDBID)
		}
		if e.PosterPath == "" {
			t.Errorf("entry %d (tmdb=%d): poster_path empty", i, e.TMDBID)
		}
		if e.ReleaseYear == 0 {
			t.Errorf("entry %d (tmdb=%d): release_year is 0", i, e.TMDBID)
		}
		if e.Dimension == "" {
			t.Errorf("entry %d (tmdb=%d): dimension empty", i, e.TMDBID)
		}
	}

	// Inactive row is excluded — fetch a wider window to confirm it
	// never surfaces, even when the limit would have admitted it by
	// display_order alone.
	wide, err := svc.ListEnriched(ctx, 100)
	if err != nil {
		t.Fatalf("ListEnriched wide: %v", err)
	}
	for _, e := range wide {
		if e.TMDBID == 13 {
			t.Errorf("inactive row (tmdb=13) should have been filtered out")
		}
	}
}

// Verifies Upsert is idempotent — the seed binary depends on this so
// re-runs don't error out and don't create duplicates.
func TestOnboarding_Upsert_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}

	root := findRepoRoot(t)
	if err := godotenv.Load(filepath.Join(root, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}

	dbCfg := config.NewDB()
	dbConn, err := db.New(*dbCfg)
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	defer dbConn.Close()

	repo := onboarding.NewRepository(dbConn)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const synthID = 999999911 // out of TMDB range, unlikely to collide
	t.Cleanup(func() {
		_, _ = dbConn.ExecContext(ctx,
			`delete from onboarding_pool where tmdb_id = $1`, synthID)
	})

	if err := repo.Upsert(ctx, onboarding.PoolEntry{
		TMDBID: synthID, Dimension: "x", DisplayOrder: 1, Active: true,
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Re-upsert with different fields — should update, not error or duplicate.
	if err := repo.Upsert(ctx, onboarding.PoolEntry{
		TMDBID: synthID, Dimension: "y", DisplayOrder: 42, Active: false,
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var count int
	if err := dbConn.GetContext(ctx, &count,
		`select count(*) from onboarding_pool where tmdb_id = $1`, synthID); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}
