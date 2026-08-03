package tracking_test

import (
	"context"
	"testing"
	"time"

	"klyvi-api/internal/tracking"
)

// The settings danger zone: reset every score in one media-type scope, or
// delete the scope outright. Both report how many rows they touched, and
// neither may leak across the media-type boundary or across users.
func TestTracking_ResetScoresScopedByMediaType(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	score := 88
	if _, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID: 550, MediaType: tracking.MediaTypeMovie, Score: &score,
	}); err != nil {
		t.Fatalf("add movie: %v", err)
	}
	season := 1
	if _, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID: 1396, MediaType: tracking.MediaTypeSeason, SeasonNumber: &season, Score: &score,
	}); err != nil {
		t.Fatalf("add season: %v", err)
	}

	affected, err := env.service.ResetScores(ctx, env.userID, tracking.MediaTypeMovie)
	if err != nil {
		t.Fatalf("ResetScores: %v", err)
	}
	if affected != 1 {
		t.Errorf("affected = %d, want 1 (movie only)", affected)
	}

	list, err := env.service.List(ctx, env.userID, tracking.ListFilters{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range list {
		switch e.MediaType {
		case tracking.MediaTypeMovie:
			if e.Score != nil {
				t.Errorf("movie score survived the reset: %v", *e.Score)
			}
		case tracking.MediaTypeSeason:
			if e.Score == nil || *e.Score != 88 {
				t.Errorf("season score was touched by a movie-scoped reset: %v", e.Score)
			}
		}
	}
}

func TestTracking_DeleteListScopedByMediaType(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID: 550, MediaType: tracking.MediaTypeMovie,
	}); err != nil {
		t.Fatalf("add movie: %v", err)
	}
	season := 1
	if _, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID: 1396, MediaType: tracking.MediaTypeSeason, SeasonNumber: &season,
	}); err != nil {
		t.Fatalf("add season: %v", err)
	}

	affected, err := env.service.DeleteList(ctx, env.userID, tracking.MediaTypeSeason)
	if err != nil {
		t.Fatalf("DeleteList: %v", err)
	}
	if affected != 1 {
		t.Errorf("affected = %d, want 1 (season only)", affected)
	}

	list, err := env.service.List(ctx, env.userID, tracking.ListFilters{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].MediaType != tracking.MediaTypeMovie {
		t.Errorf("expected only the movie to survive, got %+v", list)
	}
}

func TestTracking_DangerOpsValidateMediaType(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := env.service.ResetScores(ctx, env.userID, "tv"); err == nil {
		t.Error("ResetScores accepted media_type 'tv'")
	}
	if _, err := env.service.DeleteList(ctx, env.userID, ""); err == nil {
		t.Error("DeleteList accepted an empty media_type")
	}
}
