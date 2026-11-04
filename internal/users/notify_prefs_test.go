package users_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/users"
)

// A missing preference has to stay missing, or saving one switch would turn
// the other off.
func TestUpdateMe_PassesOnOnlyThePreferencesSent(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantPremieres *bool
		wantReleases  *bool
	}{
		{"neither", `{"bio":"x"}`, nil, nil},
		{"premieres off", `{"notify_premieres":false}`, boolPtr(false), nil},
		{"releases on", `{"notify_releases":true}`, nil, boolPtr(true)},
		{"both", `{"notify_premieres":true,"notify_releases":false}`, boolPtr(true), boolPtr(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeUserRepo{user: &users.User{ID: uuid.New(), Username: "jacob"}}
			req := httptest.NewRequest(http.MethodPatch, "/v1/users/me", strings.NewReader(tc.body))
			req = req.WithContext(middleware.WithUserUUID(req.Context(), repo.user.ID))
			rec := httptest.NewRecorder()

			users.NewAPI(users.NewService(repo)).UpdateMe(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			if !sameBool(repo.lastPatch.NotifyPremieres, tc.wantPremieres) {
				t.Errorf("NotifyPremieres = %v, want %v", repo.lastPatch.NotifyPremieres, tc.wantPremieres)
			}
			if !sameBool(repo.lastPatch.NotifyReleases, tc.wantReleases) {
				t.Errorf("NotifyReleases = %v, want %v", repo.lastPatch.NotifyReleases, tc.wantReleases)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

func sameBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func prefsTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env.dev")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip(".env.dev not found; skipping DB-dependent test")
		}
		dir = parent
	}
	if err := godotenv.Load(filepath.Join(dir, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}
	conn, err := db.New(*config.NewDB())
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// The full round trip on the real table: on by default, each switch moves on
// its own, and an edit that does not mention them leaves them alone.
func TestNotifyPrefs_RoundTrip(t *testing.T) {
	conn := prefsTestDB(t)
	ctx := context.Background()
	repo := users.NewRepository(conn)
	id := uuid.New()
	if err := repo.EnsureUser(ctx, id); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, `delete from users where id = $1`, id)
	})

	get := func(t *testing.T) (premieres, releases bool) {
		t.Helper()
		u, err := repo.GetUserByID(ctx, id)
		if err != nil {
			t.Fatalf("GetUserByID: %v", err)
		}
		return u.NotifyPremieres, u.NotifyReleases
	}

	if p, r := get(t); !p || !r {
		t.Fatalf("new user: premieres %v, releases %v, want both on", p, r)
	}

	steps := []struct {
		name          string
		patch         users.UpdateProfilePatch
		wantPremieres bool
		wantReleases  bool
	}{
		{"turn premieres off", users.UpdateProfilePatch{NotifyPremieres: boolPtr(false)}, false, true},
		{"turn releases off", users.UpdateProfilePatch{NotifyReleases: boolPtr(false)}, false, false},
		{"an unrelated edit keeps them", users.UpdateProfilePatch{Bio: strPtr("hello")}, false, false},
		{"turn both on", users.UpdateProfilePatch{NotifyPremieres: boolPtr(true), NotifyReleases: boolPtr(true)}, true, true},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			u, err := repo.UpdateProfile(ctx, id, step.patch)
			if err != nil {
				t.Fatalf("UpdateProfile: %v", err)
			}
			if u.NotifyPremieres != step.wantPremieres || u.NotifyReleases != step.wantReleases {
				t.Errorf("returned premieres %v, releases %v, want %v, %v",
					u.NotifyPremieres, u.NotifyReleases, step.wantPremieres, step.wantReleases)
			}
			if p, r := get(t); p != step.wantPremieres || r != step.wantReleases {
				t.Errorf("stored premieres %v, releases %v, want %v, %v", p, r, step.wantPremieres, step.wantReleases)
			}
		})
	}
}

// Both the profile response and the account export carry the preferences, and
// both encode the User struct.
func TestUser_EncodesThePreferences(t *testing.T) {
	data, err := json.Marshal(users.User{NotifyPremieres: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"notify_premieres":true`, `"notify_releases":false`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("user JSON missing %s: %s", want, data)
		}
	}
}
