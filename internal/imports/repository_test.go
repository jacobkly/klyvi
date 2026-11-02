package imports_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/imports"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/users"
)

// setupRepo opens the DB and creates a throwaway user whose imports are
// removed afterwards.
func setupRepo(t *testing.T) (*imports.Repository, *sqlx.DB, uuid.UUID) {
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

	userID := uuid.New()
	if err := users.NewRepository(dbConn).EnsureUser(context.Background(), userID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = dbConn.ExecContext(c, `delete from imports where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(c, `delete from users where id = $1`, userID)
	})
	return imports.NewRepository(dbConn), dbConn, userID
}

// The database holds the one-import-at-a-time rule, so it survives restarts
// and two uploads racing each other.
func TestRepository_OneActiveImportPerUser(t *testing.T) {
	repo, _, userID := setupRepo(t)
	ctx := context.Background()

	first, err := repo.Create(ctx, userID, "letterboxd")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	for _, status := range []string{imports.StatusPending, imports.StatusRunning} {
		if err := repo.SetStatus(ctx, first.ID, status, nil); err != nil {
			t.Fatalf("SetStatus %s: %v", status, err)
		}
		if _, err := repo.Create(ctx, userID, "csv"); !errors.Is(err, imports.ErrImportActive) {
			t.Errorf("Create while %s: err = %v, want ErrImportActive", status, err)
		}
	}

	if err := repo.SetStatus(ctx, first.ID, imports.StatusDone, nil); err != nil {
		t.Fatalf("SetStatus done: %v", err)
	}
	if _, err := repo.Create(ctx, userID, "csv"); err != nil {
		t.Errorf("Create after the first finished: %v", err)
	}
}

// Get already reports a running job that stopped updating as failed, so the
// user is told to try again. The retry must not then be refused by that
// same dead job.
func TestRepository_StaleJobDoesNotBlockARetry(t *testing.T) {
	repo, dbConn, userID := setupRepo(t)
	ctx := context.Background()

	dead, err := repo.Create(ctx, userID, "letterboxd")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := dbConn.ExecContext(ctx, `
		update imports set status = 'running', updated_at = now() - interval '11 minutes'
		where id = $1
	`, dead.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if _, err := repo.Create(ctx, userID, "letterboxd"); err != nil {
		t.Fatalf("retry Create: %v", err)
	}
	got, err := repo.Get(ctx, userID, dead.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != imports.StatusFailed {
		t.Errorf("dead job status = %q, want failed", got.Status)
	}
}

// Workers live in the API process, so a job left pending or running when it
// stopped will never move. Startup fails them so their owners can retry.
func TestRepository_FailUnfinished(t *testing.T) {
	repo, _, userID := setupRepo(t)
	ctx := context.Background()

	orphan, err := repo.Create(ctx, userID, "letterboxd")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	start := time.Now()
	if err := repo.FailUnfinished(ctx); err != nil {
		t.Fatalf("FailUnfinished: %v", err)
	}

	got, err := repo.Get(ctx, userID, orphan.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != imports.StatusFailed || got.Error == nil {
		t.Errorf("orphan = %q (error %v), want failed with a reason", got.Status, got.Error)
	}
	if got.UpdatedAt.Before(start.Add(-time.Minute)) {
		t.Errorf("updated_at %v was not bumped", got.UpdatedAt)
	}
	if _, err := repo.Create(ctx, userID, "letterboxd"); err != nil {
		t.Errorf("Create after the sweep: %v", err)
	}
}
