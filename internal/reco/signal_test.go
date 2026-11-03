package reco

import (
	"math"
	"testing"
)

func ptrInt(n int) *int { return &n }

// Verifies the high-stakes design rule: a low rating is negative signal,
// not just weak positive. This is the easy-to-miss bug — treating every
// rating as positive turns a 1/5 into faint praise instead of a strong
// "no" — and the test exists to guard against silent regressions into it.
func TestWeight_LowRatingIsNegative(t *testing.T) {
	cfg := DefaultConfig()

	posWeight := Weight(cfg, "rated", ptrInt(100), 0)
	if posWeight <= 0 {
		t.Errorf("rated=100 should be positive, got %f", posWeight)
	}

	negWeight := Weight(cfg, "rated", ptrInt(0), 0)
	if negWeight >= 0 {
		t.Errorf("rated=0 should be NEGATIVE, got %f (low ratings must produce negative signal)", negWeight)
	}

	neutralWeight := Weight(cfg, "rated", ptrInt(50), 0)
	if math.Abs(neutralWeight) > 1e-9 {
		t.Errorf("rated=50 should be ~0, got %f", neutralWeight)
	}
}

func TestWeight_DismissedIsNegativeRegardlessOfRating(t *testing.T) {
	cfg := DefaultConfig()
	w := Weight(cfg, "dismissed", nil, 0)
	if w >= 0 {
		t.Errorf("dismissed should be negative, got %f", w)
	}
}

func TestWeight_RecencyDecayHalvesAtHalfLife(t *testing.T) {
	cfg := DefaultConfig()

	freshWeight := Weight(cfg, "logged", nil, 0)
	stale := Weight(cfg, "logged", nil, cfg.HalfLifeDays)

	ratio := stale / freshWeight
	if math.Abs(ratio-0.5) > 0.01 {
		t.Errorf("expected weight at half-life to be ~0.5x fresh, got ratio %f", ratio)
	}
}

func TestAggregateSignal_CombinesAMoviesInteractions(t *testing.T) {
	cfg := DefaultConfig()
	rows := []InteractionRow{
		{MediaID: 1, MediaType: "movie", Kind: "logged", AgeDays: 0},
		{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: ptrInt(80), AgeDays: 30},
	}

	signal := AggregateSignal(cfg, rows)
	want := Weight(cfg, "logged", nil, 0) + Weight(cfg, "rated", ptrInt(80), 30)
	if math.Abs(signal[1]-want) > 1e-9 {
		t.Errorf("signal[1] = %f, want the sum %f", signal[1], want)
	}
}

// A season's signal belongs to its series, keyed by the series' media_id.
// Averaging the seasons keeps a strong S1 and a weak S4 from reading as two
// votes for the show.
func TestAggregateSignal_SeasonsRollUpToTheirSeriesAsAMean(t *testing.T) {
	cfg := DefaultConfig()
	const series, s1, s4 = 500, 501, 504
	mixed := AggregateSignal(cfg, []InteractionRow{
		{MediaID: s1, MediaType: "season", SeriesMediaID: series, Kind: "rated", Rating: ptrInt(90)},
		{MediaID: s4, MediaType: "season", SeriesMediaID: series, Kind: "rated", Rating: ptrInt(30)},
	})
	allHigh := AggregateSignal(cfg, []InteractionRow{
		{MediaID: s1, MediaType: "season", SeriesMediaID: series, Kind: "rated", Rating: ptrInt(90)},
		{MediaID: s4, MediaType: "season", SeriesMediaID: series, Kind: "rated", Rating: ptrInt(90)},
	})

	if mixed[series] >= allHigh[series] {
		t.Errorf("S1 high + S4 low = %f, want weaker than all seasons high = %f", mixed[series], allHigh[series])
	}
	want := (Weight(cfg, "rated", ptrInt(90), 0) + Weight(cfg, "rated", ptrInt(30), 0)) / 2
	if math.Abs(mixed[series]-want) > 1e-9 {
		t.Errorf("series signal = %f, want the season mean %f", mixed[series], want)
	}
	for _, season := range []int{s1, s4} {
		if _, ok := mixed[season]; ok {
			t.Errorf("season %d kept its own signal; it should only count toward the series", season)
		}
	}
}

// A season's own interactions add up like a movie's before the seasons are
// averaged, so logging and rating one season is one season, not two.
func TestAggregateSignal_ASeasonsInteractionsCombineBeforeTheMean(t *testing.T) {
	cfg := DefaultConfig()
	signal := AggregateSignal(cfg, []InteractionRow{
		{MediaID: 501, MediaType: "season", SeriesMediaID: 500, Kind: "logged"},
		{MediaID: 501, MediaType: "season", SeriesMediaID: 500, Kind: "rated", Rating: ptrInt(90)},
		{MediaID: 502, MediaType: "season", SeriesMediaID: 500, Kind: "rated", Rating: ptrInt(70)},
	})

	s1 := Weight(cfg, "logged", nil, 0) + Weight(cfg, "rated", ptrInt(90), 0)
	s2 := Weight(cfg, "rated", ptrInt(70), 0)
	if want := (s1 + s2) / 2; math.Abs(signal[500]-want) > 1e-9 {
		t.Errorf("series signal = %f, want %f", signal[500], want)
	}
}

func TestAggregateSignal_OneRatedSeasonEqualsTheSameRatingOnTheSeries(t *testing.T) {
	cfg := DefaultConfig()
	season := AggregateSignal(cfg, []InteractionRow{
		{MediaID: 501, MediaType: "season", SeriesMediaID: 500, Kind: "rated", Rating: ptrInt(80), AgeDays: 12},
	})
	series := AggregateSignal(cfg, []InteractionRow{
		{MediaID: 500, MediaType: "tv", Kind: "rated", Rating: ptrInt(80), AgeDays: 12},
	})

	if season[500] == 0 || season[500] != series[500] {
		t.Errorf("one rated season = %f, series-level rating = %f, want equal and non-zero", season[500], series[500])
	}
}

// Series-level interactions (a dismissed series card, say) count directly,
// on top of the season mean.
func TestAggregateSignal_SeriesInteractionsAddToTheSeasonMean(t *testing.T) {
	cfg := DefaultConfig()
	signal := AggregateSignal(cfg, []InteractionRow{
		{MediaID: 501, MediaType: "season", SeriesMediaID: 500, Kind: "rated", Rating: ptrInt(90)},
		{MediaID: 500, MediaType: "tv", Kind: "dismissed"},
	})

	want := Weight(cfg, "rated", ptrInt(90), 0) + Weight(cfg, "dismissed", nil, 0)
	if math.Abs(signal[500]-want) > 1e-9 {
		t.Errorf("series signal = %f, want %f", signal[500], want)
	}
}

// A season whose series has no media_index row has nowhere to roll up to.
func TestAggregateSignal_SkipsASeasonWithNoSeries(t *testing.T) {
	signal := AggregateSignal(DefaultConfig(), []InteractionRow{
		{MediaID: 501, MediaType: "season", Kind: "rated", Rating: ptrInt(90)},
	})
	if len(signal) != 0 {
		t.Errorf("signal = %v, want empty", signal)
	}
}

func TestSplitLikedDisliked(t *testing.T) {
	signal := map[int]float64{
		1: 0.8,  // liked
		2: -0.5, // disliked
		3: 0.0,  // dropped
	}
	liked, disliked := SplitLikedDisliked(signal)
	if liked[1] != 0.8 {
		t.Errorf("expected liked[1]=0.8, got %f", liked[1])
	}
	if disliked[2] != 0.5 {
		t.Errorf("expected disliked[2]=0.5 (magnitude), got %f", disliked[2])
	}
	if _, ok := liked[3]; ok {
		t.Error("zero signal should not appear in liked")
	}
}
