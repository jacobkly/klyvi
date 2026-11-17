package reco

import (
	"context"
	"math"
	"reflect"
	"testing"
)

// Keyword 1 ("heist") and 2 ("bank robbery") share three titles. 1 and 3
// share only two, under the floor, so they are not related.
var relatedDocs = [][]int{{1, 2}, {1, 2}, {1, 2, 2}, {1, 3}, {1, 3}, {2}, {4}}

func TestRelatedKeywords(t *testing.T) {
	r := NewRelatedKeywords(relatedDocs)
	sim := 3 / math.Sqrt(5*4) // co-count over the root of the two title counts
	for _, k := range []struct{ id, near int }{{1, 2}, {2, 1}} {
		got := r.Near(k.id)
		if len(got) != 1 || got[0].ID != k.near || math.Abs(got[0].Sim-sim) > 1e-9 {
			t.Errorf("Near(%d) = %v, want [{%d %.4f}]", k.id, got, k.near, sim)
		}
	}
	if got := r.Near(3); got != nil {
		t.Errorf("Near(3) = %v, want none: it shares two titles with 1, under the floor", got)
	}
	if got := (*RelatedKeywords)(nil).Near(1); got != nil {
		t.Errorf("a nil index has neighbours %v", got)
	}
}

func TestRelatedKeywords_KeepsTheClosest(t *testing.T) {
	// Keyword 1 appears with each of 2..13 in three titles, and with 2 in
	// three more, so 2 is the closest and only relatedTopK are kept.
	var docs [][]int
	for k := 2; k <= 13; k++ {
		for range 3 {
			docs = append(docs, []int{1, k})
		}
	}
	for range 3 {
		docs = append(docs, []int{1, 2})
	}
	got := NewRelatedKeywords(docs).Near(1)
	if len(got) != relatedTopK {
		t.Fatalf("kept %d neighbours, want %d", len(got), relatedTopK)
	}
	if got[0].ID != 2 {
		t.Errorf("closest = %d, want 2", got[0].ID)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Sim > got[i-1].Sim {
			t.Errorf("neighbours out of order: %v", got)
		}
	}
}

func TestRelatedKeywords_Expand(t *testing.T) {
	r := NewRelatedKeywords(relatedDocs)
	sim := 3 / math.Sqrt(5*4)
	got := r.Expand(map[int]float64{1: 2})
	if want := map[int]float64{2: 2 * sim}; !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %v, want %v", got, want)
	}
	// A keyword the user already has gets no partial credit on top.
	if got := r.Expand(map[int]float64{1: 2, 2: 1}); len(got) != 0 {
		t.Errorf("Expand with both keywords liked = %v, want nothing", got)
	}
	if got := (*RelatedKeywords)(nil).Expand(map[int]float64{1: 2}); got != nil {
		t.Errorf("a nil index expands to %v", got)
	}
}

func TestRelatedKeywordTerm(t *testing.T) {
	related := NewRelatedKeywords(relatedDocs)
	sim := 3 / math.Sqrt(5*4)
	feat := func(id int, keywords ...int) *MediaFeatures {
		return &MediaFeatures{MediaID: id, MediaType: "movie", GenreIDs: []int{18}, KeywordIDs: keywords,
			VoteCount: 100}
	}
	user := &UserContext{Liked: []SignalItem{{MediaID: 1, Weight: 1, Features: feat(1, 1)}}}
	cands := []Candidate{
		{MediaID: 2, MediaType: "movie", Features: feat(2, 2)}, // a related keyword
		{MediaID: 3, MediaType: "movie", Features: feat(3, 5)}, // an unrelated one
		{MediaID: 4, MediaType: "movie", Features: feat(4, 1)}, // the same keyword
	}
	saved := &gatedProfiles{release: make(chan struct{})}
	close(saved.release)
	tiers := map[string]func(Config) Scorer{
		"tier1": func(cfg Config) Scorer { return NewTier1(cfg).WithRelatedKeywords(related) },
		"tier2": func(cfg Config) Scorer { return NewTier2(cfg, saved).WithRelatedKeywords(related) },
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
		if off[2] != off[3] {
			t.Errorf("%s: weight 0 should ignore related keywords, scores = %v", name, off)
		}
		cfg.RelatedKeywordWeight = 0.5
		on := scores(cfg)
		if on[2] <= off[2] {
			t.Errorf("%s: a related keyword gained nothing: off %v, on %v", name, off[2], on[2])
		}
		if on[3] != off[3] || on[4] != off[4] {
			t.Errorf("%s: unrelated or exact matches moved: off %v, on %v", name, off, on)
		}
		if name == "tier1" {
			if want := off[2] + 0.5*sim; math.Abs(on[2]-want) > 1e-9 {
				t.Errorf("tier1: related score = %v, want %v", on[2], want)
			}
		}
	}
}

type fakeKeywords [][]int

func (f fakeKeywords) RecoKeywordSets(context.Context) ([][]int, error) { return f, nil }

func TestLoadRelatedKeywords(t *testing.T) {
	r, err := LoadRelatedKeywords(context.Background(),
		fakeKeywords(relatedDocs[:3]), fakeKeywords(relatedDocs[3:]))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Near(1); len(got) != 1 || got[0].ID != 2 {
		t.Errorf("Near(1) = %v, want films and series counted together", got)
	}
}
