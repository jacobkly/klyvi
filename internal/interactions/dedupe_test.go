package interactions_test

import (
	"context"
	"testing"
	"time"

	"klyvi-api/internal/interactions"
)

// Reloading the feed reports the same titles again. Those repeats add no
// signal, only rows, so a title keeps one impression per 24 hours.
func TestRecord_KeepsOneImpressionPerTitlePerDay(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	record := func(tmdbID int, kind string) *interactions.Interaction {
		t.Helper()
		rec, err := env.service.Record(ctx, env.userID, interactions.RecordRequest{
			TMDBID: tmdbID, MediaType: interactions.MediaTypeMovie, Kind: kind,
		})
		if err != nil {
			t.Fatalf("Record(%d, %s): %v", tmdbID, kind, err)
		}
		return rec
	}
	count := func() int {
		t.Helper()
		n, err := env.service.CountByUser(ctx, env.userID)
		if err != nil {
			t.Fatalf("CountByUser: %v", err)
		}
		return n
	}

	first := record(550, interactions.KindImpression)
	repeat := record(550, interactions.KindImpression)
	if count() != 1 {
		t.Fatalf("rows after a repeated impression = %d, want 1", count())
	}
	if repeat.ID != first.ID || !repeat.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("repeat = %d at %v, want the existing row %d at %v", repeat.ID, repeat.CreatedAt, first.ID, first.CreatedAt)
	}

	record(603, interactions.KindImpression)
	if count() != 2 {
		t.Errorf("rows after an impression on another title = %d, want 2", count())
	}

	record(550, interactions.KindClicked)
	record(550, interactions.KindClicked)
	if count() != 4 {
		t.Errorf("rows after two clicks = %d, want 4 (only impressions dedupe)", count())
	}

	if _, err := env.db.ExecContext(ctx,
		`update interactions set created_at = now() - interval '25 hours' where user_id = $1 and kind = 'impression'`,
		env.userID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	later := record(550, interactions.KindImpression)
	if later.ID == first.ID {
		t.Error("an impression past 24 hours should be a new row")
	}
	if count() != 5 {
		t.Errorf("rows after a later impression = %d, want 5", count())
	}
}
