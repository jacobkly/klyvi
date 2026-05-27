package users_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/users"

	"github.com/google/uuid"
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

// Verifies the create-on-first-auth path is safe under concurrent
// authenticated requests: N goroutines calling EnsureUser for the same
// UUID produce exactly one row, no errors.
func TestEnsureUser_ConcurrentInsertProducesOneRow(t *testing.T) {
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

	repo := users.NewRepository(dbConn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id := uuid.New() // freshly generated → not yet in DB

	cleanup := func() {
		_, _ = dbConn.ExecContext(ctx, `delete from users where id = $1`, id)
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
			if err := repo.EnsureUser(ctx, id); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("EnsureUser: %v", e)
	}

	var count int
	if err := dbConn.GetContext(ctx, &count,
		`select count(*) from users where id = $1`, id); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 users row, got %d", count)
	}

	// GetUserByID should return the row.
	u, err := repo.GetUserByID(ctx, id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u == nil {
		t.Fatal("expected user row, got nil")
	}
	if u.ID != id {
		t.Errorf("id mismatch: got %s, want %s", u.ID, id)
	}
	if u.Username == "" {
		t.Error("username should not be empty")
	}
}
