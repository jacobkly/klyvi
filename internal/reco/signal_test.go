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

func TestAggregateSignal_SkipsSeasonRows(t *testing.T) {
	cfg := DefaultConfig()
	rows := []InteractionRow{
		{MediaID: 1, MediaType: "movie", Kind: "logged", AgeDays: 0},
		{MediaID: 2, MediaType: "season", Kind: "logged", AgeDays: 0},
		{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: ptrInt(80), AgeDays: 30},
	}

	signal := AggregateSignal(cfg, rows)
	if _, ok := signal[2]; ok {
		t.Error("expected season row to be skipped (Phase 4 is movies-only)")
	}
	if signal[1] <= 0 {
		t.Errorf("expected combined positive signal for media 1, got %f", signal[1])
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
