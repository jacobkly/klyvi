package tracking_test

import (
	"context"
	"testing"
	"time"

	"klyvi-api/internal/tracking"
)

func boolPtr(b bool) *bool { return &b }

// The favorites shelf reads this flag; before it existed the client faked it
// with score >= 90. Add with favorite, flip it off, and confirm the list
// reflects the real column the whole way.
func TestTracking_FavoriteRoundTrip(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Add Fight Club as a favorite from the start.
	entry, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID:    550,
		MediaType: tracking.MediaTypeMovie,
		Favorite:  boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !entry.Favorite {
		t.Fatal("entry added with favorite=true came back false")
	}

	// The list carries the flag too — this is what the favorites shelf reads.
	list, err := env.service.List(ctx, env.userID, tracking.ListFilters{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || !list[0].Favorite {
		t.Fatalf("list did not carry favorite=true: %+v", list)
	}

	// Unfavorite via PATCH.
	updated, err := env.service.Update(ctx, env.userID, entry.MediaID, tracking.UpdateRequest{
		Favorite: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Favorite {
		t.Fatal("favorite=false patch did not stick")
	}

	// A patch that does not mention favorite leaves it alone.
	score := 85
	untouched, err := env.service.Update(ctx, env.userID, entry.MediaID, tracking.UpdateRequest{
		Score: &score,
	})
	if err != nil {
		t.Fatalf("Update score: %v", err)
	}
	if untouched.Favorite {
		t.Fatal("score-only patch flipped favorite back on")
	}
}

// An add without the field defaults to not-favorite, matching the column
// default rather than surprising older clients that never send it.
func TestTracking_FavoriteDefaultsFalse(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	entry, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID:    603, // The Matrix
		MediaType: tracking.MediaTypeMovie,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if entry.Favorite {
		t.Fatal("entry added without favorite came back true")
	}
}
