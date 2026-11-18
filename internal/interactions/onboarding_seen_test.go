package interactions_test

import (
	"context"
	"testing"
	"time"

	"klyvi-api/internal/interactions"
	"klyvi-api/internal/tracking"
)

// An onboarding answer says the user has seen the film, so it lands in the
// library as Completed with no score. The like or dislike stays a feed
// signal only, and a title already in the library is left as it was.
func TestRecord_OnboardingSeenAnswersBecomeCompletedEntries(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	trackingRepo := tracking.NewRepository(env.db)
	svc := interactions.NewService(interactions.NewRepository(env.db), trackingRepo).
		WithSeenMarker(trackingRepo)

	// 13 is already tracked as Watching with a score of its own.
	watching, own := tracking.StatusWatching, 61
	trackedID, err := trackingRepo.EnsureMediaIndexAndGetID(ctx, 13, "movie", nil)
	if err != nil {
		t.Fatalf("EnsureMediaIndexAndGetID: %v", err)
	}
	if _, err := trackingRepo.AddEntry(ctx, env.userID, trackedID, tracking.AddParams{
		MediaType: "movie", Status: &watching, Score: &own,
	}); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}

	onboarding := interactions.SourceOnboarding
	feed := interactions.SourceFeed
	liked, disliked := 85, 25
	res, err := svc.RecordBatch(ctx, env.userID, []interactions.RecordRequest{
		{TMDBID: 550, MediaType: "movie", Kind: "rated", Rating: &liked, Source: &onboarding},
		{TMDBID: 603, MediaType: "movie", Kind: "rated", Rating: &disliked, Source: &onboarding},
		{TMDBID: 13, MediaType: "movie", Kind: "rated", Rating: &liked, Source: &onboarding},
		// Not onboarding answers, so no entry.
		{TMDBID: 680, MediaType: "movie", Kind: "rated", Rating: &liked, Source: &feed},
		{TMDBID: 155, MediaType: "movie", Kind: "dismissed", Source: &onboarding},
	})
	if err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}
	if res.Accepted != 5 {
		t.Fatalf("accepted = %d, want 5", res.Accepted)
	}

	for _, tmdbID := range []int{550, 603} {
		e, err := trackingRepo.GetEntryByTMDB(ctx, env.userID, tmdbID, "movie", nil)
		if err != nil {
			t.Fatalf("GetEntryByTMDB(%d): %v", tmdbID, err)
		}
		if e == nil {
			t.Fatalf("tmdb %d: no library entry, want Completed", tmdbID)
		}
		if e.Status == nil || *e.Status != tracking.StatusCompleted {
			t.Errorf("tmdb %d: status = %v, want completed", tmdbID, e.Status)
		}
		if e.Score != nil {
			t.Errorf("tmdb %d: score = %d, want none", tmdbID, *e.Score)
		}
	}

	kept, err := trackingRepo.GetEntryByTMDB(ctx, env.userID, 13, "movie", nil)
	if err != nil || kept == nil {
		t.Fatalf("GetEntryByTMDB(13): %v, %v", kept, err)
	}
	if kept.Status == nil || *kept.Status != watching || kept.Score == nil || *kept.Score != own {
		t.Errorf("existing entry changed: status %v score %v", kept.Status, kept.Score)
	}

	for _, tmdbID := range []int{680, 155} {
		e, err := trackingRepo.GetEntryByTMDB(ctx, env.userID, tmdbID, "movie", nil)
		if err != nil {
			t.Fatalf("GetEntryByTMDB(%d): %v", tmdbID, err)
		}
		if e != nil {
			t.Errorf("tmdb %d: got a library entry, want none", tmdbID)
		}
	}
}
