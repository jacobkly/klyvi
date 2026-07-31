package users_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/users"
)

// usernameTestRepo spins up a real repository against the dev database and
// registers cleanup for the rows a test creates.
func usernameTestRepo(t *testing.T) (*users.Repository, *sqlx.DB, context.Context) {
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
	t.Cleanup(func() { dbConn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	return users.NewRepository(dbConn), dbConn, ctx
}

func newTestUser(t *testing.T, repo *users.Repository, dbConn *sqlx.DB, ctx context.Context) uuid.UUID {
	t.Helper()

	id := uuid.New()
	t.Cleanup(func() {
		_, _ = dbConn.ExecContext(context.Background(), `delete from users where id = $1`, id)
	})
	if err := repo.EnsureUser(ctx, id); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	return id
}

// shortID gives a username fragment that is unique per run without exceeding
// the 40-character limit.
func shortID() string {
	return strings.ReplaceAll(uuid.New().String()[:8], "-", "")
}

// The race the availability endpoint cannot close: two callers can both pass
// the pre-check a millisecond apart. The unique index is what actually
// decides, and this asserts the loser gets a domain error rather than the raw
// driver's "duplicate key ... (SQLSTATE 23505)" string.
func TestUpdateProfile_ConcurrentSameUsername(t *testing.T) {
	repo, dbConn, ctx := usernameTestRepo(t)

	idA := newTestUser(t, repo, dbConn, ctx)
	idB := newTestUser(t, repo, dbConn, ctx)

	wanted := "race_" + shortID()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	ids := [2]uuid.UUID{idA, idB}

	wg.Add(2)
	for i, id := range ids {
		go func() {
			defer wg.Done()
			_, errs[i] = repo.UpdateProfile(ctx, id, users.UpdateProfilePatch{Username: &wanted})
		}()
	}
	wg.Wait()

	var won, lost int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, users.ErrUsernameTaken):
			lost++
		default:
			t.Errorf("unexpected error, want ErrUsernameTaken: %v", err)
		}
	}

	if won != 1 || lost != 1 {
		t.Errorf("got %d winners and %d losers, want exactly 1 of each", won, lost)
	}

	var count int
	if err := dbConn.GetContext(ctx, &count,
		`select count(*) from users where lower(username) = lower($1)`, wanted); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 row holding %q, got %d", wanted, count)
	}
}

// The index is on lower(username), so a name differing only by case must be
// rejected the same way an exact duplicate is.
func TestUpdateProfile_RejectsCaseVariantOfTakenName(t *testing.T) {
	repo, dbConn, ctx := usernameTestRepo(t)

	idA := newTestUser(t, repo, dbConn, ctx)
	idB := newTestUser(t, repo, dbConn, ctx)

	name := "case_" + shortID()
	if _, err := repo.UpdateProfile(ctx, idA, users.UpdateProfilePatch{Username: &name}); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	upper := strings.ToUpper(name)
	_, err := repo.UpdateProfile(ctx, idB, users.UpdateProfilePatch{Username: &upper})
	if !errors.Is(err, users.ErrUsernameTaken) {
		t.Errorf("claiming %q got %v, want ErrUsernameTaken", upper, err)
	}
}

func TestUsernameExists(t *testing.T) {
	repo, dbConn, ctx := usernameTestRepo(t)

	id := newTestUser(t, repo, dbConn, ctx)
	name := "exists_" + shortID()
	if _, err := repo.UpdateProfile(ctx, id, users.UpdateProfilePatch{Username: &name}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{"exact match", name, true},
		{"upper case match", strings.ToUpper(name), true},
		{"mixed case match", strings.ToTitle(name), true},
		{"absent name", "absent_" + shortID(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.UsernameExists(ctx, tt.query)
			if err != nil {
				t.Fatalf("UsernameExists: %v", err)
			}
			if got != tt.want {
				t.Errorf("UsernameExists(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

// username_changed_at is stamped in the same statement that writes the name,
// so this pins the three cases that decide whether the cooldown is fair.
func TestUpdateProfile_StampsUsernameChangedAt(t *testing.T) {
	repo, dbConn, ctx := usernameTestRepo(t)

	id := newTestUser(t, repo, dbConn, ctx)

	// A freshly created row has never been renamed.
	before, err := repo.GetUserByID(ctx, id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if before.UsernameChangedAt != nil {
		t.Fatalf("new row has UsernameChangedAt = %v, want nil", before.UsernameChangedAt)
	}

	// A real rename stamps it.
	name := "stamp_" + shortID()
	renamed, err := repo.UpdateProfile(ctx, id, users.UpdateProfilePatch{Username: &name})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.UsernameChangedAt == nil {
		t.Fatal("rename did not stamp UsernameChangedAt")
	}
	stamped := *renamed.UsernameChangedAt

	// Re-saving the same name is not a rename and must not move the stamp,
	// or every profile save would silently restart the cooldown.
	same, err := repo.UpdateProfile(ctx, id, users.UpdateProfilePatch{Username: &name})
	if err != nil {
		t.Fatalf("resave: %v", err)
	}
	if !same.UsernameChangedAt.Equal(stamped) {
		t.Errorf("resaving the same name moved the stamp from %v to %v", stamped, *same.UsernameChangedAt)
	}

	// Neither is a capitalisation fix.
	upper := strings.ToUpper(name)
	cased, err := repo.UpdateProfile(ctx, id, users.UpdateProfilePatch{Username: &upper})
	if err != nil {
		t.Fatalf("case change: %v", err)
	}
	if !cased.UsernameChangedAt.Equal(stamped) {
		t.Errorf("a capitalisation fix moved the stamp from %v to %v", stamped, *cased.UsernameChangedAt)
	}

	// A patch that does not touch the username leaves it alone too.
	bio := "watches too much"
	bioOnly, err := repo.UpdateProfile(ctx, id, users.UpdateProfilePatch{Bio: &bio})
	if err != nil {
		t.Fatalf("bio patch: %v", err)
	}
	if !bioOnly.UsernameChangedAt.Equal(stamped) {
		t.Errorf("a bio-only patch moved the stamp from %v to %v", stamped, *bioOnly.UsernameChangedAt)
	}
}
