package tv_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/users"
)

// cleanupExec runs a cleanup statement on its own context: a test's ctx is
// already cancelled by the time t.Cleanup functions run.
func cleanupExec(dbh *sqlx.DB, query string, args ...any) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = dbh.ExecContext(ctx, query, args...)
}

// requireMigration skips when the DB under test is older than version. The
// tests share the hosted DB, where migrations are applied by hand, so a test
// for a new migration skips there until it is applied rather than failing.
func requireMigration(t *testing.T, dbh *sqlx.DB, version int64) {
	t.Helper()
	var current int64
	if err := dbh.Get(&current, `select coalesce(max(version_id), 0) from goose_db_version where is_applied`); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if current < version {
		t.Skipf("migration %05d not applied (DB is at %d)", version, current)
	}
}

// Migration 00030 projects series genres and keywords into int arrays. The
// normalizer stores TV keywords as a bare array, but rows cached before it
// unwrapped them may still hold TMDB's {"results": [...]} envelope, so the
// projection reads both.
func TestSchema_TVSeriesFeatureArraysReadBothKeywordShapes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tests := []struct {
		name         string
		tvID         int
		genres       *string
		keywords     *string
		wantGenres   string
		wantKeywords string
	}{
		{
			name:         "bare keyword array",
			tvID:         2_100_000_003,
			genres:       strPtr(`[{"id": 18, "name": "Drama"}, {"id": 80, "name": "Crime"}]`),
			keywords:     strPtr(`[{"id": 1, "name": "a"}, {"id": 2, "name": "b"}]`),
			wantGenres:   "18,80",
			wantKeywords: "1,2",
		},
		{
			name:         "keywords still in the results envelope",
			tvID:         2_100_000_004,
			genres:       strPtr(`[{"id": 10765, "name": "Sci-Fi & Fantasy"}]`),
			keywords:     strPtr(`{"results": [{"id": 3, "name": "c"}]}`),
			wantGenres:   "10765",
			wantKeywords: "3",
		},
		{
			name:         "nothing cached",
			tvID:         2_100_000_005,
			wantGenres:   "",
			wantKeywords: "",
		},
		{
			name:         "json null",
			tvID:         2_100_000_006,
			genres:       strPtr(`null`),
			keywords:     strPtr(`null`),
			wantGenres:   "",
			wantKeywords: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := func() { cleanupExec(dbh, `delete from tv_series where tv_id = $1`, tt.tvID) }
			cleanup()
			t.Cleanup(cleanup)

			if _, err := dbh.ExecContext(ctx,
				`insert into tv_series (tv_id, genres, keywords) values ($1, $2::jsonb, $3::jsonb)`,
				tt.tvID, tt.genres, tt.keywords); err != nil {
				t.Fatalf("insert: %v", err)
			}

			var genres, keywords string
			if err := dbh.QueryRowContext(ctx, `
				select array_to_string(genre_ids, ','), array_to_string(keyword_ids, ',')
				from tv_series where tv_id = $1`, tt.tvID).Scan(&genres, &keywords); err != nil {
				t.Fatalf("read arrays: %v", err)
			}
			if genres != tt.wantGenres || keywords != tt.wantKeywords {
				t.Errorf("genre_ids = {%s}, keyword_ids = {%s}; want {%s} and {%s}",
					genres, keywords, tt.wantGenres, tt.wantKeywords)
			}
		})
	}
}

// A series gets one media_index row of type 'tv', keyed by its TMDB id with
// no season number, however many times it is ensured.
func TestSchema_MediaIndexHoldsOneRowPerSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_007
	cleanup := func() { cleanupExec(dbh, `delete from media_index where id = $1 and media_type = 'tv'`, tvID) }
	cleanup()
	t.Cleanup(cleanup)

	for range 2 {
		if _, err := dbh.ExecContext(ctx, `
			insert into media_index (id, media_type) values ($1, 'tv')
			on conflict (id, season_number, media_type) do nothing`, tvID); err != nil {
			t.Fatalf("insert series row: %v", err)
		}
	}

	var count int
	if err := dbh.GetContext(ctx, &count,
		`select count(*) from media_index where id = $1 and media_type = 'tv'`, tvID); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("series rows = %d, want 1", count)
	}
}

// Only a season carries a season number. A series row with one would dodge
// the uniqueness above, and a season without one could not be resolved.
func TestSchema_MediaIndexRejectsMisshapenRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const id = 2_100_000_008
	cleanup := func() { cleanupExec(dbh, `delete from media_index where id = $1`, id) }
	cleanup()
	t.Cleanup(cleanup)

	tests := []struct {
		name         string
		mediaType    string
		seasonNumber *int
	}{
		{"series with a season number", "tv", intPtr(1)},
		{"season without a season number", "season", nil},
		{"movie with a season number", "movie", intPtr(1)},
		{"unknown media type", "episode", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := dbh.ExecContext(ctx,
				`insert into media_index (id, season_number, media_type) values ($1, $2, $3)`,
				id, tt.seasonNumber, tt.mediaType)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Errorf("insert error = %v, want a check violation (23514)", err)
			}
		})
	}
}

// Feed signal (impressions, clicks, dismissals, saves) can target a series.
func TestSchema_InteractionsAcceptSeriesSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_009
	userID := uuid.New()
	if err := users.NewRepository(dbh).EnsureUser(ctx, userID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	t.Cleanup(func() {
		cleanupExec(dbh, `delete from interactions where user_id = $1`, userID)
		cleanupExec(dbh, `delete from users where id = $1`, userID)
		cleanupExec(dbh, `delete from media_index where id = $1 and media_type = 'tv'`, tvID)
	})

	var mediaID int
	if err := dbh.GetContext(ctx, &mediaID, `
		insert into media_index (id, media_type) values ($1, 'tv')
		on conflict (id, season_number, media_type) do update set media_type = excluded.media_type
		returning media_id`, tvID); err != nil {
		t.Fatalf("insert series row: %v", err)
	}

	if _, err := dbh.ExecContext(ctx, `
		insert into interactions (user_id, media_id, media_type, kind, source)
		values ($1, $2, 'tv', 'impression', 'feed')`, userID, mediaID); err != nil {
		t.Errorf("series impression rejected: %v", err)
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
