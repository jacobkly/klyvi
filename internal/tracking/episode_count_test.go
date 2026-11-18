package tracking_test

import (
	"context"
	"testing"
	"time"

	"klyvi-api/internal/tracking"
)

// The episode cap reads the cached season's episode list. A movie, or a season
// whose episodes aren't cached, has no known count and so no cap.
func TestRepository_EpisodeCount(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 990000417
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = env.db.ExecContext(ctx, `delete from tv_seasons where tv_id = $1`, tvID)
		_, _ = env.db.ExecContext(ctx, `delete from media_index where id = $1 and media_type = 'season'`, tvID)
	})
	if _, err := env.db.ExecContext(ctx, `
		insert into tv_seasons (tv_id, season_number, episodes) values
			($1, 1, '[{"episode_number":1},{"episode_number":2},{"episode_number":3}]'::jsonb),
			($1, 2, null)
	`, tvID); err != nil {
		t.Fatalf("seed seasons: %v", err)
	}

	mediaID := func(mediaType string, tmdbID int, season *int) int {
		t.Helper()
		id, err := env.repo.EnsureMediaIndexAndGetID(ctx, tmdbID, mediaType, season)
		if err != nil {
			t.Fatalf("EnsureMediaIndexAndGetID: %v", err)
		}
		return id
	}
	one, two, three := 1, 2, 3

	tests := []struct {
		name    string
		mediaID int
		want    *int
	}{
		{"cached season", mediaID(tracking.MediaTypeSeason, tvID, &one), &three},
		{"season without episodes", mediaID(tracking.MediaTypeSeason, tvID, &two), nil},
		{"uncached season", mediaID(tracking.MediaTypeSeason, tvID, &three), nil},
		{"movie", mediaID(tracking.MediaTypeMovie, 550, nil), nil},
		{"unknown media id", -1, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := env.repo.EpisodeCount(ctx, tc.mediaID)
			if err != nil {
				t.Fatalf("EpisodeCount: %v", err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("got %d, want no count", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Fatalf("got %v, want %d", got, *tc.want)
			}
		})
	}
}
