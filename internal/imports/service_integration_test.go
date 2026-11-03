package imports_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/imports"
	"klyvi-api/internal/interactions"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tracking"
	"klyvi-api/internal/tv"
	"klyvi-api/internal/users"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
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

func setupImports(t *testing.T) (*imports.Service, *sqlx.DB, uuid.UUID) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB + TMDB integration test in short mode")
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

	tmdbClient := tmdb.NewClient(config.NewTMDB().APIKey)

	userRepo := users.NewRepository(dbConn)
	trackingRepo := tracking.NewRepository(dbConn)
	trackingSvc := tracking.NewService(trackingRepo)
	interactionsSvc := interactions.NewService(interactions.NewRepository(dbConn), trackingRepo)
	movieSvc := movies.NewService(tmdbClient, movies.NewRepository(dbConn))
	svc := imports.NewService(imports.NewRepository(dbConn), tmdbClient, movieSvc, trackingSvc, interactionsSvc).
		WithSeries(tv.NewService(tmdbClient, tv.NewRepository(dbConn)))

	userID := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := userRepo.EnsureUser(ctx, userID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = dbConn.ExecContext(c, `delete from imports where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(c, `delete from media_list where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(c, `delete from interactions where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(c, `delete from users where id = $1`, userID)
	})

	return svc, dbConn, userID
}

// The whole job end to end: a small Letterboxd file goes in, the worker
// matches the titles against TMDB, writes tracking rows, and the job reports
// done with a matched count. This is the proof the async pipeline composes.
func TestImport_LetterboxdEndToEnd(t *testing.T) {
	svc, dbConn, userID := setupImports(t)
	ctx := context.Background()

	file := "Date,Name,Year,Rating\n" +
		"2024-01-02,Parasite,2019,4.5\n" +
		"2024-01-03,The Matrix,1999,5\n" +
		"2024-01-04,Zqxwv Not A Real Film Title,2019,3\n"

	imp, err := svc.Start(ctx, userID, string(imports.SourceLetterboxd), []byte(file))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if imp.Status != imports.StatusPending {
		t.Errorf("initial status = %q, want pending", imp.Status)
	}

	// Poll to completion (the worker runs in a goroutine).
	var final *imports.Import
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		final, err = svc.Get(ctx, userID, imp.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if final.Status == imports.StatusDone || final.Status == imports.StatusFailed {
			break
		}
		time.Sleep(1 * time.Second)
	}

	if final == nil || final.Status != imports.StatusDone {
		t.Fatalf("job did not finish: %+v", final)
	}
	if final.Total != 3 {
		t.Errorf("total = %d, want 3", final.Total)
	}
	// The two real films match; the nonsense title does not — and is counted,
	// not dropped.
	if final.Matched < 2 {
		t.Errorf("matched = %d, want at least 2", final.Matched)
	}
	if final.Matched+final.Unmatched != final.Total {
		t.Errorf("matched(%d)+unmatched(%d) != total(%d)", final.Matched, final.Unmatched, final.Total)
	}

	// The nonsense title is surfaced, not just counted, so the client can show
	// it and let the user find it manually.
	if final.Unmatched < 1 {
		t.Errorf("unmatched = %d, want at least 1", final.Unmatched)
	}
	var foundNonsense bool
	for _, ut := range final.Titles {
		if strings.Contains(ut.Title, "Zqxwv") {
			foundNonsense = true
		}
	}
	if !foundNonsense {
		t.Errorf("unmatched_titles should include the nonsense title, got %+v", final.Titles)
	}

	// The matched films actually landed as tracking rows.
	var trackCount int
	if err := dbConn.GetContext(ctx, &trackCount,
		`select count(*) from media_list where user_id = $1 and status = 'completed'`, userID); err != nil {
		t.Fatalf("count tracking: %v", err)
	}
	if trackCount < 2 {
		t.Errorf("tracking rows = %d, want at least 2", trackCount)
	}
}

// waitForImport polls a job until it finishes.
func waitForImport(t *testing.T, svc *imports.Service, userID, id uuid.UUID) *imports.Import {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		imp, err := svc.Get(ctx, userID, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if imp.Status == imports.StatusDone || imp.Status == imports.StatusFailed {
			return imp
		}
		time.Sleep(time.Second)
	}
	t.Fatal("import did not finish in time")
	return nil
}

// The trimmed Trakt export, zipped the way Trakt hands it over, goes through
// real TMDB and the database: the finished miniseries lands as a completed
// season, the show started lands as watching, and the show rating becomes a
// series-level signal.
func TestImport_TraktExportEndToEnd(t *testing.T) {
	svc, dbConn, userID := setupImports(t)
	ctx := context.Background()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files, err := filepath.Glob(filepath.Join("testdata", "trakt-export", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixture files: %v (found %d)", err, len(files))
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := zw.Create(filepath.Base(path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	imp, err := svc.Start(ctx, userID, string(imports.SourceTrakt), buf.Bytes())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitForImport(t, svc, userID, imp.ID)
	if final.Status != imports.StatusDone {
		t.Fatalf("job did not finish: %+v", final)
	}
	// The obscure film has no TMDB id and may or may not find a match by
	// search, so only the totals are pinned.
	if final.Total != 6 || final.Matched+final.Unmatched != final.Total || final.Matched < 5 {
		t.Errorf("total/matched/unmatched = %d/%d/%d, want 6 with at least 5 matched",
			final.Total, final.Matched, final.Unmatched)
	}

	type season struct {
		Status   string `db:"status"`
		Progress *int   `db:"episode_progress"`
	}
	seasonOf := func(tvID, number int) season {
		t.Helper()
		var s season
		if err := dbConn.GetContext(ctx, &s, `
			select ml.status, ml.episode_progress
			from media_list ml
			join media_index mi on mi.media_id = ml.media_id
			where ml.user_id = $1 and mi.id = $2 and mi.season_number = $3 and mi.media_type = 'season'`,
			userID, tvID, number); err != nil {
			t.Fatalf("season %d of tv %d: %v", number, tvID, err)
		}
		return s
	}
	if got := seasonOf(87108, 1); got.Status != "completed" || got.Progress == nil || *got.Progress != 5 {
		t.Errorf("Chernobyl season 1 = %s with progress %v, want completed with 5", got.Status, got.Progress)
	}
	if got := seasonOf(95396, 1); got.Status != "watching" || got.Progress == nil || *got.Progress != 3 {
		t.Errorf("Severance season 1 = %s with progress %v, want watching with 3", got.Status, got.Progress)
	}

	var showRatings int
	if err := dbConn.GetContext(ctx, &showRatings, `
		select count(*)
		from interactions i
		join media_index mi on mi.media_id = i.media_id
		where i.user_id = $1 and mi.id = 95396 and i.media_type = 'tv' and i.kind = 'rated' and i.rating = 80`,
		userID); err != nil {
		t.Fatalf("count show ratings: %v", err)
	}
	if showRatings != 1 {
		t.Errorf("Severance show ratings = %d, want 1", showRatings)
	}
}
