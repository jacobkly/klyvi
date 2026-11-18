package tracking_test

import (
	"context"
	"testing"
	"time"

	"klyvi-api/internal/tracking"
)

// The backfill turns past onboarding answers into Completed entries with no
// score, the same way new answers are filed. A dry run counts without
// writing, and a title already in the library is left alone.
func TestBackfillOnboardingSeen(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = env.db.ExecContext(ctx, `delete from interactions where user_id = $1`, env.userID)
	})

	mediaID := func(tmdbID int) int {
		id, err := env.repo.EnsureMediaIndexAndGetID(ctx, tmdbID, "movie", nil)
		if err != nil {
			t.Fatalf("EnsureMediaIndexAndGetID(%d): %v", tmdbID, err)
		}
		return id
	}
	answer := func(tmdbID, rating int, source string) {
		if _, err := env.db.ExecContext(ctx, `
			insert into interactions (user_id, media_id, media_type, kind, rating, source)
			values ($1, $2, 'movie', 'rated', $3, $4)
		`, env.userID, mediaID(tmdbID), rating, source); err != nil {
			t.Fatalf("insert interaction: %v", err)
		}
	}
	answer(550, 85, "onboarding")
	answer(550, 85, "onboarding") // the same answer twice is one entry
	answer(603, 25, "onboarding")
	answer(13, 85, "onboarding")
	answer(680, 85, "feed")

	watching, own := tracking.StatusWatching, 61
	if _, err := env.repo.AddEntry(ctx, env.userID, mediaID(13), tracking.AddParams{
		MediaType: "movie", Status: &watching, Score: &own,
	}); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}

	dry, err := env.repo.BackfillOnboardingSeen(ctx, &env.userID, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Entries != 2 || dry.Accounts != 1 {
		t.Fatalf("dry run = %+v, want 2 entries for 1 account", dry)
	}
	if e, _ := env.repo.GetEntryByTMDB(ctx, env.userID, 550, "movie", nil); e != nil {
		t.Fatal("the dry run wrote an entry")
	}

	got, err := env.repo.BackfillOnboardingSeen(ctx, &env.userID, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got.Entries != 2 || got.Accounts != 1 {
		t.Fatalf("backfill = %+v, want 2 entries for 1 account", got)
	}
	for _, tmdbID := range []int{550, 603} {
		e, err := env.repo.GetEntryByTMDB(ctx, env.userID, tmdbID, "movie", nil)
		if err != nil || e == nil {
			t.Fatalf("tmdb %d: entry %v, err %v", tmdbID, e, err)
		}
		if e.Status == nil || *e.Status != tracking.StatusCompleted || e.Score != nil {
			t.Errorf("tmdb %d: status %v score %v, want completed with no score", tmdbID, e.Status, e.Score)
		}
	}
	kept, _ := env.repo.GetEntryByTMDB(ctx, env.userID, 13, "movie", nil)
	if kept == nil || kept.Status == nil || *kept.Status != watching || kept.Score == nil || *kept.Score != own {
		t.Errorf("existing entry changed: %+v", kept)
	}
	if e, _ := env.repo.GetEntryByTMDB(ctx, env.userID, 680, "movie", nil); e != nil {
		t.Error("a feed rating became an entry")
	}

	again, err := env.repo.BackfillOnboardingSeen(ctx, &env.userID, false)
	if err != nil || again.Entries != 0 {
		t.Errorf("second run = %+v, %v, want nothing new", again, err)
	}

	// With no user, the dry run counts every account.
	if _, err := env.repo.BackfillOnboardingSeen(ctx, nil, true); err != nil {
		t.Errorf("dry run for every account: %v", err)
	}
}
