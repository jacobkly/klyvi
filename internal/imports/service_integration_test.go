package imports_test

import (
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
	svc := imports.NewService(imports.NewRepository(dbConn), tmdbClient, movieSvc, trackingSvc, interactionsSvc)

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
