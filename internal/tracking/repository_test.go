package tracking_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/tracking"
	"klyvi-api/internal/users"

	"github.com/google/uuid"
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

// trackingTestEnv carries a fresh user + repo handles plus a cleanup hook.
// Every test gets its own user so the rows the test writes are easy to reset.
type trackingTestEnv struct {
	db       *sqlx.DB
	service  *tracking.Service
	repo     *tracking.Repository
	userRepo *users.Repository
	userID   uuid.UUID
}

func setupTracking(t *testing.T) *trackingTestEnv {
	t.Helper()

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
	t.Cleanup(func() { _ = dbConn.Close() })

	userRepo := users.NewRepository(dbConn)
	repo := tracking.NewRepository(dbConn)
	svc := tracking.NewService(repo)

	userID := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := userRepo.EnsureUser(ctx, userID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = dbConn.ExecContext(ctx, `delete from media_list where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(ctx, `delete from users where id = $1`, userID)
	})

	return &trackingTestEnv{
		db:       dbConn,
		service:  svc,
		repo:     repo,
		userRepo: userRepo,
		userID:   userID,
	}
}

func TestTracking_FullMovieCycle(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const movieID = 550 // Fight Club

	score95 := 95
	status := tracking.StatusCompleted
	entry, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID:    movieID,
		MediaType: tracking.MediaTypeMovie,
		Status:    &status,
		Score:     &score95,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if entry.MediaType != tracking.MediaTypeMovie {
		t.Errorf("media_type mismatch: got %q", entry.MediaType)
	}
	if entry.Status == nil || *entry.Status != tracking.StatusCompleted {
		t.Errorf("status mismatch: got %v", entry.Status)
	}
	if entry.Score == nil || *entry.Score != 95 {
		t.Errorf("score mismatch: got %v", entry.Score)
	}

	// Idempotent re-add — same entry comes back.
	again, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID:    movieID,
		MediaType: tracking.MediaTypeMovie,
		Status:    &status,
		Score:     &score95,
	})
	if err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	if again.ID != entry.ID {
		t.Errorf("re-Add should return existing entry (id %d), got id %d", entry.ID, again.ID)
	}

	// Patch the status + score.
	newStatus := tracking.StatusRewatching
	newScore := 92
	updated, err := env.service.Update(ctx, env.userID, entry.MediaID, tracking.UpdateRequest{
		Status: &newStatus,
		Score:  &newScore,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated == nil {
		t.Fatal("Update returned nil")
	}
	if updated.Status == nil || *updated.Status != tracking.StatusRewatching {
		t.Errorf("status after update: %v", updated.Status)
	}
	if updated.Score == nil || *updated.Score != 92 {
		t.Errorf("score after update: %v", updated.Score)
	}

	// List should see exactly this one entry.
	entries, err := env.service.List(ctx, env.userID, tracking.ListFilters{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}

	// Delete works; second delete is a 404 (returns false).
	ok, err := env.service.Delete(ctx, env.userID, entry.MediaID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !ok {
		t.Error("Delete: expected true (deleted), got false")
	}
	ok2, err := env.service.Delete(ctx, env.userID, entry.MediaID)
	if err != nil {
		t.Fatalf("Delete repeat: %v", err)
	}
	if ok2 {
		t.Error("repeat Delete should return false; got true")
	}
}

func TestTracking_AddSeasonResolvesMediaIndex(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 1396
	const seasonNum = 1
	sn := seasonNum
	status := tracking.StatusWatching

	entry, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID:       tvID,
		MediaType:    tracking.MediaTypeSeason,
		SeasonNumber: &sn,
		Status:       &status,
	})
	if err != nil {
		t.Fatalf("Add season: %v", err)
	}
	if entry.MediaType != tracking.MediaTypeSeason {
		t.Errorf("media_type mismatch: got %q", entry.MediaType)
	}

	// Verify the media_index row points back to (tv_id=1396, season_number=1, media_type='season').
	var rowTVID, rowSeason int
	var rowType string
	if err := env.db.QueryRowxContext(ctx,
		`select id, season_number, media_type from media_index where media_id = $1`,
		entry.MediaID).Scan(&rowTVID, &rowSeason, &rowType); err != nil {
		t.Fatalf("inspect media_index: %v", err)
	}
	if rowTVID != tvID || rowSeason != seasonNum || rowType != "season" {
		t.Errorf("media_index row mismatch: id=%d season=%d type=%q (want %d/%d/season)",
			rowTVID, rowSeason, rowType, tvID, seasonNum)
	}
}

func TestTracking_EnsureMediaIndexAndGetID_Concurrent(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Synthetic id outside TMDB's plausible range but under the int4 max
	// (2_147_483_647) since media_index.id is INTEGER.
	const testTMDBID = 2_100_000_777

	cleanup := func() {
		_, _ = env.db.ExecContext(ctx,
			`delete from media_index where id = $1 and media_type = 'movie'`, testTMDBID)
	}
	cleanup()
	t.Cleanup(cleanup)

	const n = 10
	results := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			id, err := env.repo.EnsureMediaIndexAndGetID(ctx, testTMDBID, tracking.MediaTypeMovie, nil)
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			results[i] = id
		}(i)
	}
	wg.Wait()

	// All concurrent calls must converge on a single media_id, even though
	// the SERIAL might increment on conflicting INSERTs.
	first := results[0]
	for i, id := range results {
		if id == 0 {
			t.Errorf("goroutine %d got id 0", i)
		}
		if id != first {
			t.Errorf("inconsistent media_id: goroutine %d got %d, expected %d", i, id, first)
		}
	}

	var count int
	if err := env.db.GetContext(ctx, &count,
		`select count(*) from media_index where id = $1 and media_type = 'movie'`, testTMDBID); err != nil {
		t.Fatalf("count media_index: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 media_index row, got %d", count)
	}
}
