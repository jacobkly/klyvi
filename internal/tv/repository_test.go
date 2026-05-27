package tv_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tv"

	"github.com/jmoiron/sqlx"
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

// setup wires up a service + repo against the real DB and TMDB, returning
// the underlying *sqlx.DB so individual tests can run direct cleanup and
// verification SQL.
func setup(t *testing.T) (*tv.Service, *tv.Repository, *sqlx.DB) {
	t.Helper()

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
	t.Cleanup(func() { _ = dbConn.Close() })

	tmdbCfg := config.NewTMDB()
	repo := tv.NewRepository(dbConn)
	svc := tv.NewService(tmdb.NewClient(tmdbCfg.APIKey), repo)

	return svc, repo, dbConn
}

// First TV-side test. Verifies the slice-2 1A fix carries over to the season
// path: concurrent EnsureMediaIndexSeason calls for the same (tv, season)
// must produce exactly one row.
func TestEnsureMediaIndexSeason_ConcurrentInsertProducesOneRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}

	_, repo, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Synthetic ids well outside TMDB's plausible range to avoid colliding
	// with real catalog data.
	const testTVID = 2_100_000_001
	const testSeasonNum = 7

	cleanup := func() {
		_, _ = dbh.ExecContext(ctx,
			`delete from media_index where id = $1 and season_number = $2 and media_type = 'season'`,
			testTVID, testSeasonNum)
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
			if err := repo.EnsureMediaIndexSeason(ctx, testTVID, testSeasonNum); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("EnsureMediaIndexSeason: %v", e)
	}

	var count int
	if err := dbh.GetContext(ctx, &count,
		`select count(*) from media_index where id = $1 and season_number = $2 and media_type = 'season'`,
		testTVID, testSeasonNum); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 media_index row, got %d", count)
	}
}

// Verifies the end-to-end series-cache flow: fetching a fresh series persists
// a tv_series row with keywords (unwrapped from the TV-specific "results"
// envelope) and credits.
func TestService_GetTvById_PopulatesSeriesKeywordsAndCredits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TMDB+DB integration test in short mode")
	}

	svc, _, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 1396 // Breaking Bad — stable, rich keywords + credits.

	_, _ = dbh.ExecContext(ctx, `delete from tv_series where tv_id = $1`, tvID)

	res, err := svc.GetTvById(ctx, "external", tvID, 0)
	if err != nil {
		t.Fatalf("GetTvById: %v", err)
	}
	series, ok := res.(*tv.TVSeries)
	if !ok {
		t.Fatalf("expected *TVSeries, got %T", res)
	}
	if series.Keywords == nil {
		t.Error("Keywords on returned series is nil")
	}
	if series.Credits == nil {
		t.Error("Credits on returned series is nil")
	}

	var keywords, credits []byte
	if err := dbh.QueryRowContext(ctx,
		`select keywords, credits from tv_series where tv_id = $1`, tvID).
		Scan(&keywords, &credits); err != nil {
		t.Fatalf("read persisted row: %v", err)
	}

	// Keywords must be the unwrapped array, not the {"results": [...]} wrapper.
	var kw []map[string]any
	if err := json.Unmarshal(keywords, &kw); err != nil {
		t.Fatalf("keywords is not a JSON array (still wrapped?): %v\nraw: %s", err, keywords)
	}
	if len(kw) == 0 {
		t.Error("expected non-empty keywords for tv_id=1396")
	} else if _, ok := kw[0]["id"]; !ok {
		t.Errorf("first keyword missing id field: %+v", kw[0])
	}

	var cr map[string]any
	if err := json.Unmarshal(credits, &cr); err != nil {
		t.Fatalf("credits is not a JSON object: %v", err)
	}
	if _, ok := cr["cast"]; !ok {
		t.Error("credits.cast missing")
	}
}

// Verifies the end-to-end season-cache flow: fetching a fresh season persists
// a tv_seasons row AND a media_index row tagged as 'season'.
func TestService_GetTvById_PopulatesSeasonAndMediaIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TMDB+DB integration test in short mode")
	}

	svc, _, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 1396
	const seasonNum = 1

	// Reset state so we exercise the fetch+persist path.
	_, _ = dbh.ExecContext(ctx,
		`delete from tv_seasons where tv_id = $1 and season_number = $2`, tvID, seasonNum)
	_, _ = dbh.ExecContext(ctx,
		`delete from media_index where id = $1 and season_number = $2 and media_type = 'season'`,
		tvID, seasonNum)

	res, err := svc.GetTvById(ctx, "external", tvID, seasonNum)
	if err != nil {
		t.Fatalf("GetTvById season: %v", err)
	}
	season, ok := res.(*tv.TVSeason)
	if !ok {
		t.Fatalf("expected *TVSeason, got %T", res)
	}
	if season.TVID != tvID || season.SeasonNumber != seasonNum {
		t.Errorf("season meta mismatch: got tv=%d season=%d, want %d/%d",
			season.TVID, season.SeasonNumber, tvID, seasonNum)
	}

	var seasonCount, indexCount int
	if err := dbh.GetContext(ctx, &seasonCount,
		`select count(*) from tv_seasons where tv_id = $1 and season_number = $2`,
		tvID, seasonNum); err != nil {
		t.Fatalf("count tv_seasons: %v", err)
	}
	if seasonCount != 1 {
		t.Errorf("expected 1 tv_seasons row, got %d", seasonCount)
	}

	if err := dbh.GetContext(ctx, &indexCount,
		`select count(*) from media_index where id = $1 and season_number = $2 and media_type = 'season'`,
		tvID, seasonNum); err != nil {
		t.Fatalf("count media_index: %v", err)
	}
	if indexCount != 1 {
		t.Errorf("expected 1 media_index row for tv=%d season=%d, got %d", tvID, seasonNum, indexCount)
	}
}
