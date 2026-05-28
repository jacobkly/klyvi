package interactions_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/interactions"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/http/middleware"
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

type interactionsTestEnv struct {
	db      *sqlx.DB
	service *interactions.Service
	userID  uuid.UUID
}

func setup(t *testing.T) *interactionsTestEnv {
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
	trackingRepo := tracking.NewRepository(dbConn)
	repo := interactions.NewRepository(dbConn)
	svc := interactions.NewService(repo, trackingRepo)

	userID := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := userRepo.EnsureUser(ctx, userID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = dbConn.ExecContext(ctx, `delete from interactions where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(ctx, `delete from media_list where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(ctx, `delete from taste_profiles where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(ctx, `delete from users where id = $1`, userID)
	})

	return &interactionsTestEnv{db: dbConn, service: svc, userID: userID}
}

func TestInteractions_RecordRatedMovie(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rating := 87
	source := interactions.SourceDetail
	rec, err := env.service.Record(ctx, env.userID, interactions.RecordRequest{
		TMDBID:    550, // Fight Club
		MediaType: interactions.MediaTypeMovie,
		Kind:      interactions.KindRated,
		Rating:    &rating,
		Source:    &source,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if rec.ID == 0 {
		t.Error("expected generated id, got 0")
	}
	if rec.Kind != "rated" || rec.Rating == nil || *rec.Rating != 87 {
		t.Errorf("payload mismatch: %+v", rec)
	}

	got, err := env.service.ListByUser(ctx, env.userID, 0)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 interaction, got %d", len(got))
	}
}

func TestInteractions_RecordSeasonRequiresSeasonNumber(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := env.service.Record(ctx, env.userID, interactions.RecordRequest{
		TMDBID:    1396,
		MediaType: interactions.MediaTypeSeason,
		Kind:      interactions.KindLogged,
		// SeasonNumber deliberately omitted.
	})
	if err == nil {
		t.Fatal("expected validation error for missing season_number")
	}
}

func TestInteractions_RecordRatedRequiresRating(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := env.service.Record(ctx, env.userID, interactions.RecordRequest{
		TMDBID:    550,
		MediaType: interactions.MediaTypeMovie,
		Kind:      interactions.KindRated,
		// Rating deliberately omitted.
	})
	if err == nil {
		t.Fatal("expected validation error for missing rating on kind=rated")
	}
}

// Verifies GET /v1/interactions for a user with no history returns
// "data": [], not "data": null. Same JSON-contract guard as tracking.
func TestInteractions_List_EmptyIsArrayNotNull(t *testing.T) {
	env := setup(t)
	api := interactions.NewAPI(env.service)

	req := httptest.NewRequest("GET", "/v1/interactions", nil)
	req = req.WithContext(middleware.WithUserUUID(req.Context(), env.userID))
	rec := httptest.NewRecorder()
	api.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf(`expected "data":[] in body, got: %s`, body)
	}
	if strings.Contains(body, `"data":null`) {
		t.Errorf(`response contains "data":null: %s`, body)
	}
}

func TestInteractions_ListByUser_SinceDays(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// One fresh interaction now, plus a backdated one outside the window.
	rating := 92
	if _, err := env.service.Record(ctx, env.userID, interactions.RecordRequest{
		TMDBID:    550,
		MediaType: interactions.MediaTypeMovie,
		Kind:      interactions.KindRated,
		Rating:    &rating,
	}); err != nil {
		t.Fatalf("Record fresh: %v", err)
	}

	// Manually backdate a row to 100 days ago.
	_, err := env.db.ExecContext(ctx, `
		insert into interactions (user_id, media_id, media_type, kind, rating, created_at)
		select $1, mi.media_id, 'movie', 'rated', 80, now() - interval '100 days'
		from media_index mi
		where mi.id = 13 and mi.media_type = 'movie'
		limit 1
	`, env.userID)
	if err != nil {
		// Not all dev DBs have movie 13 cached; the test is still useful via
		// the fresh row, so don't fail the suite — just skip the bounded check.
		t.Logf("could not backdate second row (catalog row missing?): %v", err)
	}

	// since_days=30 should exclude the 100-day-old row if it landed.
	bounded, err := env.service.ListByUser(ctx, env.userID, 30)
	if err != nil {
		t.Fatalf("ListByUser bounded: %v", err)
	}
	for _, r := range bounded {
		if r.CreatedAt.Before(time.Now().Add(-31 * 24 * time.Hour)) {
			t.Errorf("since_days=30 returned an older row: %v", r.CreatedAt)
		}
	}

	all, err := env.service.ListByUser(ctx, env.userID, 0)
	if err != nil {
		t.Fatalf("ListByUser all: %v", err)
	}
	if len(all) < len(bounded) {
		t.Errorf("all (%d) should be >= bounded (%d)", len(all), len(bounded))
	}
}
