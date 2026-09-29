package tracking_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/tracking"
)

// ensuredSeries records the series it is asked to cache, or fails every call
// when fail is set.
type ensuredSeries struct {
	ids  []int
	fail bool
}

func (e *ensuredSeries) EnsureSeries(_ context.Context, tvID int) error {
	e.ids = append(e.ids, tvID)
	if e.fail {
		return errors.New("tmdb down")
	}
	return nil
}

func TestAdd_EnsuresTheSeriesBehindATrackedSeason(t *testing.T) {
	two := 2
	cases := []struct {
		name string
		req  tracking.AddRequest
		want []int
	}{
		{"season", tracking.AddRequest{TMDBID: 1396, MediaType: tracking.MediaTypeSeason, SeasonNumber: &two}, []int{1396}},
		{"movie", tracking.AddRequest{TMDBID: 550, MediaType: tracking.MediaTypeMovie}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			series := &ensuredSeries{}
			svc := tracking.NewService(&memTracking{}).WithSeriesEnsurer(series)

			if _, err := svc.Add(context.Background(), uuid.New(), tc.req); err != nil {
				t.Fatalf("Add: %v", err)
			}
			if !slices.Equal(series.ids, tc.want) {
				t.Errorf("ensured series %v, want %v", series.ids, tc.want)
			}
		})
	}
}

// The user's list is theirs. A catalog miss must not cost them the entry.
func TestAdd_KeepsTheEntryWhenTheSeriesCannotBeCached(t *testing.T) {
	two := 2
	svc := tracking.NewService(&memTracking{}).WithSeriesEnsurer(&ensuredSeries{fail: true})

	entry, err := svc.Add(context.Background(), uuid.New(), tracking.AddRequest{
		TMDBID: 1396, MediaType: tracking.MediaTypeSeason, SeasonNumber: &two,
	})
	if err != nil || entry == nil {
		t.Errorf("Add = %v, %v; want the entry and no error", entry, err)
	}
}
