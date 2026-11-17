package reco

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
)

func TestShrink(t *testing.T) {
	for _, tc := range []struct {
		users int
		k     float64
		want  float64
	}{
		{0, 10, 0},
		{1, 10, 1.0 / 11},
		{10, 10, 0.5},
		{90, 10, 0.9},
		{5, 0, 1},
	} {
		if got := Shrink(tc.users, tc.k); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("Shrink(%d, %v) = %v, want %v", tc.users, tc.k, got, tc.want)
		}
	}
}

func TestCoLikedScores(t *testing.T) {
	liked := []SignalItem{{MediaID: 1, Weight: 2}, {MediaID: 2, Weight: 1}}
	near := map[int][]CoLiked{
		1: {{ID: 10, Users: 10, Sim: 0.5}},
		2: {{ID: 10, Users: 90, Sim: 1}, {ID: 11, Users: 10, Sim: 1}},
	}
	got := CoLikedScores(liked, near, 10)
	// Each neighbour is credited sim times n/(n+k), weighted by the liked
	// title it came from, over the total liked weight.
	want := map[int]float64{
		10: (2*0.5*0.5 + 1*1*0.9) / 3,
		11: 1 * 1 * 0.5 / 3,
	}
	if len(got) != len(want) {
		t.Fatalf("scores = %v, want %v", got, want)
	}
	for id, w := range want {
		if math.Abs(got[id]-w) > 1e-12 {
			t.Errorf("title %d = %v, want %v", id, got[id], w)
		}
	}
	if got := CoLikedScores(liked, nil, 10); len(got) != 0 {
		t.Errorf("no counts should score nothing, got %v", got)
	}
}

// fakeCoLiked answers from a fixed table and records the ids it was asked for.
type fakeCoLiked struct {
	near  map[int][]CoLiked
	err   error
	asked []int
}

func (f *fakeCoLiked) CoLiked(_ context.Context, ids []int) (map[int][]CoLiked, error) {
	f.asked = append(f.asked, ids...)
	if f.err != nil {
		return nil, f.err
	}
	out := map[int][]CoLiked{}
	for _, id := range ids {
		if n, ok := f.near[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func coLikedTiers() map[string]func(Config, CoLikedSource) Scorer {
	saved := &gatedProfiles{release: make(chan struct{})}
	close(saved.release)
	return map[string]func(Config, CoLikedSource) Scorer{
		"tier1": func(cfg Config, src CoLikedSource) Scorer { return NewTier1(cfg).WithCoLiked(src) },
		"tier2": func(cfg Config, src CoLikedSource) Scorer { return NewTier2(cfg, saved).WithCoLiked(src) },
	}
}

func coLikedUser() (*UserContext, []Candidate) {
	user := &UserContext{
		Liked:    []SignalItem{{MediaID: 1, Weight: 1, Features: walkFeat(1, 100, []int{1, 2}, nil)}},
		Disliked: []SignalItem{{MediaID: 2, Weight: 1, Features: walkFeat(2, 100, []int{7}, nil)}},
	}
	cands := []Candidate{
		{MediaID: 10, MediaType: "movie", Features: walkFeat(10, 100, []int{1}, nil)},
		{MediaID: 14, MediaType: "movie", Features: walkFeat(14, 100, []int{9}, nil)},
	}
	return user, cands
}

// With no counts, or counts that fail to load, the feed is exactly what it
// is with the term off.
func TestCoLikedTerm_EmptyIsIdentical(t *testing.T) {
	user, cands := coLikedUser()
	for name, tier := range coLikedTiers() {
		off, err := tier(DefaultConfig(), nil).Score(context.Background(), user, cands)
		if err != nil {
			t.Fatal(err)
		}
		on := DefaultConfig()
		on.CoLikedWeight = 1
		for label, src := range map[string]CoLikedSource{
			"no source":     nil,
			"empty table":   &fakeCoLiked{},
			"failing table": &fakeCoLiked{err: errors.New("down")},
		} {
			got, err := tier(on, src).Score(context.Background(), user, cands)
			if err != nil {
				t.Fatalf("%s %s: %v", name, label, err)
			}
			if !reflect.DeepEqual(got, off) {
				t.Errorf("%s %s: feed changed\n got %+v\nwant %+v", name, label, got, off)
			}
		}
	}
}

func TestCoLikedTerm(t *testing.T) {
	user, cands := coLikedUser()
	for name, tier := range coLikedTiers() {
		scores := func(cfg Config, src CoLikedSource) map[int]float64 {
			t.Helper()
			out, err := tier(cfg, src).Score(context.Background(), user, cands)
			if err != nil {
				t.Fatal(err)
			}
			m := map[int]float64{}
			for _, s := range out {
				m[s.MediaID] = s.Score
			}
			return m
		}
		off := scores(DefaultConfig(), nil)

		cfg := DefaultConfig()
		cfg.CoLikedWeight = 1
		cfg.CoLikedShrink = 10
		src := &fakeCoLiked{near: map[int][]CoLiked{1: {{ID: 14, Users: 10, Sim: 1}}}}
		on := scores(cfg, src)

		if want := off[14] + 0.5; math.Abs(on[14]-want) > 1e-9 {
			t.Errorf("%s: co-liked title = %v, want %v", name, on[14], want)
		}
		if on[10] != off[10] {
			t.Errorf("%s: a title nobody co-liked moved: %v to %v", name, off[10], on[10])
		}
		if !slices.Equal(src.asked, []int{1}) {
			t.Errorf("%s: asked for %v, want only the liked title", name, src.asked)
		}
	}
}
