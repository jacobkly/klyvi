package reco

import (
	"context"
	"math"
	"reflect"
	"testing"
)

// Keyword 1 tags three titles and keyword 2 two, so 2 is the rarer node.
// Person 100 is in two casts.
var (
	walkKeywordSets = [][]int{{1, 2}, {1, 9}, {1}, {2}}
	walkCastSets    = [][]int{{100}, {100, 200}}
)

func walkFeat(id, votes int, keywords, cast []int) *MediaFeatures {
	return &MediaFeatures{MediaID: id, MediaType: "movie", GenreIDs: []int{18}, KeywordIDs: keywords,
		CastIDs: cast, VoteCount: votes}
}

func walkScores(t *testing.T, g *WalkGraph, beta float64) map[int]float64 {
	t.Helper()
	liked := []SignalItem{{MediaID: 1, Weight: 1, Features: walkFeat(1, 100, []int{1, 2}, []int{100})}}
	cands := []Candidate{
		{MediaID: 10, Features: walkFeat(10, 100, []int{1}, nil)},        // shares keyword 1
		{MediaID: 11, Features: walkFeat(11, 10000, []int{1}, nil)},      // the same, far more voted
		{MediaID: 12, Features: walkFeat(12, 100, []int{2}, nil)},        // shares the rarer keyword 2
		{MediaID: 13, Features: walkFeat(13, 100, []int{9}, []int{100})}, // shares the actor
		{MediaID: 14, Features: walkFeat(14, 100, []int{9}, nil)},        // shares nothing
		{MediaID: 15}, // no features
	}
	got := g.Scores(liked, cands, beta)
	if len(got) != len(cands) {
		t.Fatalf("got %d scores for %d candidates", len(got), len(cands))
	}
	out := map[int]float64{}
	for i, c := range cands {
		out[c.MediaID] = got[i]
	}
	return out
}

func TestWalkGraph_Scores(t *testing.T) {
	g := NewWalkGraph(walkKeywordSets, walkCastSets)

	flat := walkScores(t, g, 0)
	if flat[10] != flat[11] {
		t.Errorf("beta 0: popularity should not matter, %v vs %v", flat[10], flat[11])
	}
	if !(flat[12] > flat[10]) {
		t.Errorf("a rarer shared keyword should walk further: %v vs %v", flat[12], flat[10])
	}
	if flat[13] <= 0 {
		t.Errorf("a shared actor should score, got %v", flat[13])
	}
	if flat[14] != 0 || flat[15] != 0 {
		t.Errorf("titles sharing nothing scored %v and %v", flat[14], flat[15])
	}
	var top float64
	for _, s := range flat {
		top = math.Max(top, s)
	}
	if top != 1 {
		t.Errorf("scores should be scaled so the best is 1, best = %v", top)
	}

	pen := walkScores(t, g, 0.5)
	// 100 times the votes at beta 0.5 is a tenth of the score.
	if ratio := pen[10] / pen[11]; math.Abs(ratio-10) > 1e-9 {
		t.Errorf("beta 0.5: score ratio for 100x the votes = %v, want 10", ratio)
	}
}

func TestWalkGraph_NilWalksWithEvenDegrees(t *testing.T) {
	got := walkScores(t, nil, 0)
	if got[10] != got[12] {
		t.Errorf("a nil graph should weigh every node the same, %v vs %v", got[10], got[12])
	}
	if got[14] != 0 {
		t.Errorf("a title sharing nothing scored %v", got[14])
	}
}

func TestWalkTerm(t *testing.T) {
	g := NewWalkGraph(walkKeywordSets, walkCastSets)
	user := &UserContext{Liked: []SignalItem{{MediaID: 1, Weight: 1, Features: walkFeat(1, 100, []int{1, 2}, []int{100})}}}
	cands := []Candidate{
		{MediaID: 10, MediaType: "movie", Features: walkFeat(10, 100, []int{1}, nil)},
		{MediaID: 14, MediaType: "movie", Features: walkFeat(14, 100, []int{9}, nil)},
	}
	saved := &gatedProfiles{release: make(chan struct{})}
	close(saved.release)
	tiers := map[string]func(Config) Scorer{
		"tier1": func(cfg Config) Scorer { return NewTier1(cfg).WithWalk(g) },
		"tier2": func(cfg Config) Scorer { return NewTier2(cfg, saved).WithWalk(g) },
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
		cfg.WalkBeta = 0
		off := scores(cfg)
		cfg.WalkWeight = 0.5
		on := scores(cfg)
		// Title 10 is the only one the walk reaches, so it scores 1 before the weight.
		if want := off[10] + 0.5; math.Abs(on[10]-want) > 1e-9 {
			t.Errorf("%s: walked title score = %v, want %v", name, on[10], want)
		}
		if on[14] != off[14] {
			t.Errorf("%s: a title the walk does not reach moved: %v to %v", name, off[14], on[14])
		}
	}
}

type fakeCast map[int][]int

func (f fakeCast) RecoCast(context.Context, int) (map[int][]int, error) { return f, nil }

func TestLoadCastIndex(t *testing.T) {
	cast, err := LoadCastIndex(context.Background(), fakeCast{550: {287, 819}}, fakeCast{550: {17419}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cast.IDs("movie", 550); !reflect.DeepEqual(got, []int{287, 819}) {
		t.Errorf("film cast = %v", got)
	}
	if got := cast.IDs("tv", 550); !reflect.DeepEqual(got, []int{17419}) {
		t.Errorf("series cast = %v", got)
	}
	if got := cast.Sets(); len(got) != 2 {
		t.Errorf("Sets = %v, want one per title", got)
	}
}
