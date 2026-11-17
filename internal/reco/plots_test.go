package reco

import (
	"context"
	"math"
	"testing"
)

func unit(v ...float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / math.Sqrt(n))
	}
	return out
}

func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func TestPlotIndex(t *testing.T) {
	// Every vector leans the same way on the first axis, as a small sentence
	// model's do. Centering takes that shared lean out.
	x := NewPlotIndex([]TitlePlot{
		{MediaType: "movie", TMDBID: 1, Vector: unit(1, 1, 0)},
		{MediaType: "movie", TMDBID: 2, Vector: unit(1, -1, 0)},
		{MediaType: "tv", TMDBID: 1, Vector: unit(1, 0, 1)},
		{MediaType: "tv", TMDBID: 2, Vector: unit(1, 0, -1)},
		{MediaType: "movie", TMDBID: 3, Vector: []float32{0, 0, 0}},
		{MediaType: "movie", TMDBID: 4, Vector: unit(1, 1)},
	})
	if x.Len() != 4 {
		t.Errorf("Len = %d, want the 4 usable vectors (a zero vector and a wrong length are dropped)", x.Len())
	}
	a, b := x.Vector("movie", 1), x.Vector("movie", 2)
	if a == nil || b == nil || x.Vector("movie", 3) != nil || x.Vector("movie", 99) != nil {
		t.Fatalf("Vector lookups: %v %v %v", a, b, x.Vector("movie", 3))
	}
	if got := dot(a, a); math.Abs(got-1) > 1e-6 {
		t.Errorf("a centered vector has length² %v, want 1", got)
	}
	if got := dot(a, b); got > -0.99 {
		t.Errorf("after centering, opposite plots have cosine %v, want -1", got)
	}
	if got := dot(a, x.Vector("tv", 1)); math.Abs(got) > 1e-6 {
		t.Errorf("unrelated plots have cosine %v, want 0", got)
	}

	var none *PlotIndex
	if none.Len() != 0 || none.Vector("movie", 1) != nil {
		t.Error("a nil index should hold nothing")
	}
}

func TestPlotScore(t *testing.T) {
	liked := [][]float32{unit(1, 0), unit(0, 1), unit(-1, 0)}
	cases := []struct {
		name  string
		cand  []float32
		liked [][]float32
		want  float64
	}{
		{"no likes with plots", unit(1, 0), nil, 0},
		{"no plot", nil, liked, 0},
		{"one like, the same story", unit(1, 0), liked[:1], 1},
		// The three nearest are 1, 0 and -1, and the negative counts as 0.
		{"mean of the nearest, never below 0", unit(1, 0), liked, 1.0 / 3},
		{"between two likes", unit(1, 1), liked[:2], math.Sqrt(0.5)},
	}
	for _, c := range cases {
		if got := plotScore(c.cand, c.liked); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%s: plotScore = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPooledPlots_KeepsTheStrongestLikes(t *testing.T) {
	var items []SignalItem
	for i := 0; i < maxPlotLikes+20; i++ {
		items = append(items, SignalItem{MediaID: i, Weight: float64(i), Features: &MediaFeatures{Plot: unit(1, float32(i))}})
	}
	items = append(items,
		SignalItem{MediaID: -1, Weight: 1000, Features: &MediaFeatures{}},
		SignalItem{MediaID: -2, Weight: 1000},
	)
	got := pooledPlots(items)
	if len(got) != maxPlotLikes {
		t.Fatalf("pooled %d plots, want %d", len(got), maxPlotLikes)
	}
	if got[0][1] != unit(1, float32(maxPlotLikes+19))[1] {
		t.Error("the strongest like should come first")
	}
}

// The plot term is off at weight 0, titles without a plot vector score as
// they did before it at any weight, and a title with the liked title's story
// gains the whole weight.
func TestPlotTerm(t *testing.T) {
	feat := func(id int, plot []float32) *MediaFeatures {
		return &MediaFeatures{MediaID: id, MediaType: "movie", GenreIDs: []int{18}, KeywordIDs: []int{1},
			VoteCount: 100, Plot: plot}
	}
	user := &UserContext{Liked: []SignalItem{{MediaID: 1, Weight: 1, Features: feat(1, unit(1, 0))}}}
	cands := []Candidate{
		{MediaID: 2, MediaType: "movie", Features: feat(2, unit(1, 0))}, // the same story
		{MediaID: 3, MediaType: "movie", Features: feat(3, unit(0, 1))}, // an unrelated story
		{MediaID: 4, MediaType: "movie", Features: feat(4, nil)},        // no Wikipedia plot
	}
	saved := &gatedProfiles{release: make(chan struct{})}
	close(saved.release)
	tiers := map[string]func(Config) Scorer{
		"tier1": func(cfg Config) Scorer { return NewTier1(cfg) },
		"tier2": func(cfg Config) Scorer { return NewTier2(cfg, saved) },
	}
	for name, tier := range tiers {
		scores := func(cfg Config) map[int]float64 {
			t.Helper()
			out, err := tier(cfg).Score(context.Background(), user, cands)
			if err != nil {
				t.Fatal(err)
			}
			m := map[int]float64{}
			for _, s := range out {
				m[s.MediaID] = s.Score
			}
			return m
		}
		cfg := DefaultConfig()
		off := scores(cfg)
		if off[2] != off[3] || off[3] != off[4] {
			t.Errorf("%s: weight 0 should ignore plots, scores = %v", name, off)
		}
		cfg.PlotWeight = 0.5
		on := scores(cfg)
		if want := off[2] + 0.5; math.Abs(on[2]-want) > 1e-6 {
			t.Errorf("%s: same-story score = %v, want %v", name, on[2], want)
		}
		if on[3] != off[3] || on[4] != off[4] {
			t.Errorf("%s: titles with no shared story moved: off %v, on %v", name, off, on)
		}
	}
}

// A disliked title's story counts against a candidate in Tier 1, as its
// genres and keywords do.
func TestPlotTerm_DislikedStory(t *testing.T) {
	feat := func(id int, plot []float32) *MediaFeatures {
		return &MediaFeatures{MediaID: id, MediaType: "movie", GenreIDs: []int{18}, KeywordIDs: []int{1},
			VoteCount: 100, Plot: plot}
	}
	user := &UserContext{
		Liked:    []SignalItem{{MediaID: 1, Weight: 1, Features: feat(1, unit(1, 0))}},
		Disliked: []SignalItem{{MediaID: 5, Weight: 1, Features: feat(5, unit(0, 1))}},
	}
	cands := []Candidate{
		{MediaID: 2, MediaType: "movie", Features: feat(2, unit(1, 1))},
		{MediaID: 3, MediaType: "movie", Features: feat(3, unit(1, -1))},
	}
	cfg := DefaultConfig()
	cfg.PlotWeight = 1
	out, err := NewTier1(cfg).Score(context.Background(), user, cands)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].MediaID != 3 {
		t.Errorf("the title nearer the disliked story should rank lower, got %+v", out)
	}
}

func TestPlotIndex_Nearest(t *testing.T) {
	x := NewPlotIndex([]TitlePlot{
		{MediaType: "movie", TMDBID: 1, Vector: unit(1, 0, 0)},
		{MediaType: "movie", TMDBID: 2, Vector: unit(0.9, 0.1, 0)},
		{MediaType: "movie", TMDBID: 3, Vector: unit(0.5, 0.5, 0)},
		{MediaType: "movie", TMDBID: 4, Vector: unit(-1, 0, 0)},
		{MediaType: "tv", TMDBID: 5, Vector: unit(1, 0, 0)},
		{MediaType: "movie", TMDBID: 6, Vector: unit(0, -1, 0)},
		{MediaType: "movie", TMDBID: 7, Vector: unit(0, 0, 1)},
		{MediaType: "movie", TMDBID: 8, Vector: unit(0, 0, -1)},
	})
	got := x.Nearest("movie", 1, 10)
	if len(got) == 0 || got[0].TMDBID != 2 {
		t.Fatalf("Nearest = %+v, want title 2 first", got)
	}
	for i, n := range got {
		if n.TMDBID == 1 || n.TMDBID == 5 || n.TMDBID == 4 || n.Sim <= 0 {
			t.Errorf("Nearest holds %+v: itself, another media type or an opposite story", n)
		}
		if i > 0 && n.Sim > got[i-1].Sim {
			t.Errorf("Nearest is out of order at %d: %+v", i, got)
		}
	}
	if len(x.Nearest("movie", 1, 1)) != 1 || x.Nearest("movie", 99, 5) != nil {
		t.Error("Nearest should cap at k and be nil for a title with no plot")
	}

	if s := x.Similarity("movie", 1, 2); math.Abs(s-got[0].Sim) > 1e-9 {
		t.Errorf("Similarity(1, 2) = %v, want %v", s, got[0].Sim)
	}
	if x.Similarity("movie", 1, 4) != 0 || x.Similarity("movie", 1, 99) != 0 {
		t.Error("Similarity should be 0 for opposite stories and missing plots")
	}
}
