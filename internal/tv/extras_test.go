package tv_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"klyvi-api/internal/tv"
)

// A series detail fetch asks TMDB for the extras in the same request and
// stores the rating, the trailer and the IMDb id.
func TestGetTvById_StoresTheExtras(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 41)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_041
	resetShow(t, dbh, tvID)
	client := &fakeTMDB{}
	repo := tv.NewRepository(dbh)
	svc := tv.NewService(client, repo)

	if _, err := svc.GetTvById(ctx, "external", tvID, 0); err != nil {
		t.Fatalf("GetTvById: %v", err)
	}
	if len(client.calls) != 1 || !strings.Contains(client.calls[0], "content_ratings,videos,external_ids") {
		t.Errorf("calls = %v, want one detail request with the extras appended", client.calls)
	}

	got, err := repo.GetTVSeriesByID(ctx, tvID)
	if err != nil || got == nil {
		t.Fatalf("GetTVSeriesByID: %v, %v", got, err)
	}
	if got.Certification == nil || *got.Certification != "TV-MA" {
		t.Errorf("certification = %v, want TV-MA", got.Certification)
	}
	if got.TrailerKey == nil || *got.TrailerKey != "HhesaQXLuRY" {
		t.Errorf("trailer key = %v, want HhesaQXLuRY", got.TrailerKey)
	}
	if got.IMDBID == nil || *got.IMDBID != "tt0903747" {
		t.Errorf("imdb id = %v, want tt0903747", got.IMDBID)
	}
	if !got.HasExtras {
		t.Error("has_extras = false, want true")
	}
}

// A full series cached before the extras is refetched once on its first
// view. After that the cache answers.
func TestGetTvById_FillsTheExtrasOfAnOlderRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 41)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_042
	resetShow(t, dbh, tvID)
	client := &fakeTMDB{}
	repo := tv.NewRepository(dbh)
	svc := tv.NewService(client, repo)
	if err := svc.EnsureSeries(ctx, tvID); err != nil {
		t.Fatalf("EnsureSeries: %v", err)
	}
	if _, err := dbh.ExecContext(ctx, `
		update tv_series set certification = null, trailer_key = null, imdb_id = null, has_extras = false
		where tv_id = $1`, tvID); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	for range 2 {
		if _, err := svc.GetTvById(ctx, "external", tvID, 0); err != nil {
			t.Fatalf("GetTvById: %v", err)
		}
	}

	if n := client.seriesCalls(); n != 2 {
		t.Errorf("series fetched %d times, want 2 (the first cache, then one refill)", n)
	}
	got, err := repo.GetTVSeriesByID(ctx, tvID)
	if err != nil || got == nil {
		t.Fatalf("GetTVSeriesByID: %v, %v", got, err)
	}
	if got.Certification == nil || *got.Certification != "TV-MA" || !got.HasExtras {
		t.Errorf("certification = %v, has_extras = %v; want TV-MA and true", got.Certification, got.HasExtras)
	}
}
