package interactions_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/interactions"
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

func TestRecord_SeriesIdentity(t *testing.T) {
	one := 1
	cases := []struct {
		name    string
		req     interactions.RecordRequest
		wantErr bool
	}{
		{"series signal", interactions.RecordRequest{TMDBID: 1396, MediaType: "tv", Kind: "saved"}, false},
		{"series with a season number", interactions.RecordRequest{TMDBID: 1396, MediaType: "tv", SeasonNumber: &one, Kind: "saved"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := interactions.NewService(&memInteractions{}, tmdbIDAsMediaID{})
			_, err := svc.Record(context.Background(), uuid.New(), tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Record error = %v, want error %v", err, tc.wantErr)
			}
			var ve interactions.ValidationError
			if tc.wantErr && !errors.As(err, &ve) {
				t.Errorf("error %v is not a ValidationError", err)
			}
		})
	}
}

// Deliberate signal on a season or a series needs the series' features to
// mean anything, so it caches the series. Passive feed signal is about
// candidates the catalog already holds.
func TestRecord_EnsuresTheSeriesBehindDeliberateTVSignal(t *testing.T) {
	two := 2
	cases := []struct {
		name string
		req  interactions.RecordRequest
		want []int
	}{
		{"rated season", interactions.RecordRequest{TMDBID: 1396, MediaType: "season", SeasonNumber: &two, Kind: "rated", Rating: intPtr(90)}, []int{1396}},
		{"saved series", interactions.RecordRequest{TMDBID: 1396, MediaType: "tv", Kind: "saved"}, []int{1396}},
		{"series impression", interactions.RecordRequest{TMDBID: 1396, MediaType: "tv", Kind: "impression"}, nil},
		{"series click", interactions.RecordRequest{TMDBID: 1396, MediaType: "tv", Kind: "clicked"}, nil},
		{"rated movie", interactions.RecordRequest{TMDBID: 550, MediaType: "movie", Kind: "rated", Rating: intPtr(90)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			series := &ensuredSeries{}
			svc := interactions.NewService(&memInteractions{}, tmdbIDAsMediaID{}).WithSeriesEnsurer(series)

			if _, err := svc.Record(context.Background(), uuid.New(), tc.req); err != nil {
				t.Fatalf("Record: %v", err)
			}
			if !slices.Equal(series.ids, tc.want) {
				t.Errorf("ensured series %v, want %v", series.ids, tc.want)
			}
		})
	}
}

// The signal is the user's. A catalog miss must not cost them the write.
func TestRecord_KeepsTheSignalWhenTheSeriesCannotBeCached(t *testing.T) {
	svc := interactions.NewService(&memInteractions{}, tmdbIDAsMediaID{}).
		WithSeriesEnsurer(&ensuredSeries{fail: true})

	if _, err := svc.Record(context.Background(), uuid.New(), interactions.RecordRequest{
		TMDBID: 1396, MediaType: "tv", Kind: "saved",
	}); err != nil {
		t.Errorf("Record: %v", err)
	}
}
