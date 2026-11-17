package reco

import (
	"context"
	"math"
	"reflect"
	"testing"
)

func TestFactIndex(t *testing.T) {
	x := NewFactIndex([]TitleFacts{
		{MediaType: "movie", TMDBID: 1, IDs: []int{10, 20}},
		{MediaType: "movie", TMDBID: 2, IDs: []int{10}},
		{MediaType: "movie", TMDBID: 3, IDs: []int{30}},
		{MediaType: "tv", TMDBID: 1, IDs: []int{20}},
	})

	if got := x.IDs("movie", 1); !reflect.DeepEqual(got, []int{10, 20}) {
		t.Errorf("IDs(movie 1) = %v", got)
	}
	if got := x.IDs("tv", 1); !reflect.DeepEqual(got, []int{20}) {
		t.Errorf("IDs(tv 1) = %v, want the show's own facts", got)
	}
	if got := x.IDs("movie", 99); got != nil {
		t.Errorf("IDs(unmatched) = %v, want nil", got)
	}

	// Three films carry facts: 10 is on two of them, 20 on one.
	if got, want := x.Weight("movie", 10), math.Log(4.0/3.0)+1; math.Abs(got-want) > 1e-9 {
		t.Errorf("Weight(movie 10) = %v, want %v", got, want)
	}
	if got, want := x.Weight("movie", 20), math.Log(4.0/2.0)+1; math.Abs(got-want) > 1e-9 {
		t.Errorf("Weight(movie 20) = %v, want %v", got, want)
	}
	if x.Weight("movie", 20) <= x.Weight("movie", 10) {
		t.Error("a rarer fact should weigh more")
	}
	// Shows are counted over their own catalog.
	if got, want := x.Weight("tv", 20), math.Log(2.0/2.0)+1; math.Abs(got-want) > 1e-9 {
		t.Errorf("Weight(tv 20) = %v, want %v", got, want)
	}

	var none *FactIndex
	if none.IDs("movie", 1) != nil || none.Weight("movie", 10) != 1 {
		t.Error("a nil index should hold no facts and weigh everything 1")
	}
}

// fakeCatalog answers every candidate read with the same titles.
type fakeCatalog struct {
	CatalogRepository
	cands []Candidate
}

func (f fakeCatalog) SampleMovieCandidates(context.Context, int) ([]Candidate, error) {
	return f.cands, nil
}
func (f fakeCatalog) SampleSeriesCandidates(context.Context, int) ([]Candidate, error) {
	return f.cands, nil
}
func (f fakeCatalog) CandidatesByTaste(context.Context, []int, []int, int, int) ([]Candidate, error) {
	return f.cands, nil
}
func (f fakeCatalog) SeriesCandidatesByTaste(context.Context, []int, []int, int, int) ([]Candidate, error) {
	return f.cands, nil
}
func (f fakeCatalog) CandidatesByMediaIDs(context.Context, []int) ([]Candidate, error) {
	return f.cands, nil
}

func TestFactCatalog(t *testing.T) {
	shared := &MediaFeatures{MediaType: "movie", KeywordIDs: []int{5}}
	inner := fakeCatalog{cands: []Candidate{
		{MediaID: 1, MediaType: "movie", TMDBID: 550, Features: shared},
		{MediaID: 2, MediaType: "movie", TMDBID: 551, Features: &MediaFeatures{MediaType: "movie"}},
		{MediaID: 3, MediaType: "tv", TMDBID: 550},
	}}
	x := NewFactIndex([]TitleFacts{
		{MediaType: "movie", TMDBID: 550, IDs: []int{7, 8}},
		{MediaType: "tv", TMDBID: 550, IDs: []int{9}},
	})
	crew := NewFactIndex([]TitleFacts{{MediaType: "movie", TMDBID: 550, IDs: []int{7467}}})
	cat := NewFactCatalog(inner, x, crew)
	ctx := context.Background()

	reads := map[string]func() ([]Candidate, error){
		"sample films":    func() ([]Candidate, error) { return cat.SampleMovieCandidates(ctx, 1) },
		"sample series":   func() ([]Candidate, error) { return cat.SampleSeriesCandidates(ctx, 1) },
		"films by taste":  func() ([]Candidate, error) { return cat.CandidatesByTaste(ctx, nil, nil, 0, 1) },
		"series by taste": func() ([]Candidate, error) { return cat.SeriesCandidatesByTaste(ctx, nil, nil, 0, 1) },
		"by media ids":    func() ([]Candidate, error) { return cat.CandidatesByMediaIDs(ctx, nil) },
	}
	for name, read := range reads {
		got, err := read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ids := got[0].Features.FactIDs; !reflect.DeepEqual(ids, []int{7, 8}) {
			t.Errorf("%s: matched film facts = %v, want [7 8]", name, ids)
		}
		if ids := got[0].Features.CrewIDs; !reflect.DeepEqual(ids, []int{7467}) {
			t.Errorf("%s: matched film crew = %v, want [7467]", name, ids)
		}
		if got[0].Features.KeywordIDs[0] != 5 {
			t.Errorf("%s: the film lost its other features", name)
		}
		if got[1].Features.FactIDs != nil || got[1].Features.CrewIDs != nil {
			t.Errorf("%s: an unmatched film gained facts %v", name, got[1].Features.FactIDs)
		}
		if got[2].Features != nil {
			t.Errorf("%s: a title without features gained some", name)
		}
	}
	if shared.FactIDs != nil || shared.CrewIDs != nil || inner.cands[0].Features != shared {
		t.Error("the inner catalog's titles were changed in place")
	}
}

// The fact term is off at weight 0, and titles with no facts score exactly as
// they did before it, at any weight.
func TestTier1FactTerm(t *testing.T) {
	x := NewFactIndex([]TitleFacts{
		{MediaType: "movie", TMDBID: 1, IDs: []int{100}},
		{MediaType: "movie", TMDBID: 2, IDs: []int{100}},
		{MediaType: "movie", TMDBID: 3, IDs: []int{200}},
	})
	feat := func(id int, facts ...int) *MediaFeatures {
		return &MediaFeatures{MediaID: id, MediaType: "movie", GenreIDs: []int{18}, KeywordIDs: []int{1},
			VoteCount: 100, FactIDs: facts}
	}
	user := &UserContext{Liked: []SignalItem{{MediaID: 1, Weight: 1, Features: feat(1, 100)}}}
	cands := []Candidate{
		{MediaID: 2, MediaType: "movie", Features: feat(2, 100)}, // shares the liked film's director
		{MediaID: 3, MediaType: "movie", Features: feat(3, 200)}, // facts, none shared
		{MediaID: 4, MediaType: "movie", Features: feat(4)},      // no Wikidata match
	}
	scores := func(cfg Config) map[int]float64 {
		t.Helper()
		out, err := NewTier1(cfg).WithFacts(x).Score(context.Background(), user, cands)
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
		t.Errorf("weight 0 should ignore facts, scores = %v", off)
	}
	cfg.FactWeight = 0.5
	on := scores(cfg)
	if want := off[2] + 0.5*x.Weight("movie", 100); math.Abs(on[2]-want) > 1e-9 {
		t.Errorf("shared fact score = %v, want %v", on[2], want)
	}
	if on[3] != off[3] || on[4] != off[4] {
		t.Errorf("titles sharing no facts moved: off %v, on %v", off, on)
	}
}

// The crew term works like the fact term on its own ids and weight: off at 0,
// and titles that share no crew with the likes score as before.
func TestCrewTerm(t *testing.T) {
	crew := NewFactIndex([]TitleFacts{
		{MediaType: "movie", TMDBID: 1, IDs: []int{525}},
		{MediaType: "movie", TMDBID: 2, IDs: []int{525}},
		{MediaType: "movie", TMDBID: 3, IDs: []int{138}},
	})
	feat := func(id int, people ...int) *MediaFeatures {
		return &MediaFeatures{MediaID: id, MediaType: "movie", GenreIDs: []int{18}, KeywordIDs: []int{1},
			VoteCount: 100, CrewIDs: people}
	}
	user := &UserContext{Liked: []SignalItem{{MediaID: 1, Weight: 1, Features: feat(1, 525)}}}
	cands := []Candidate{
		{MediaID: 2, MediaType: "movie", Features: feat(2, 525)}, // same director
		{MediaID: 3, MediaType: "movie", Features: feat(3, 138)}, // crew, none shared
		{MediaID: 4, MediaType: "movie", Features: feat(4)},      // no credits cached
	}
	saved := &gatedProfiles{release: make(chan struct{})}
	close(saved.release)
	tiers := map[string]func(Config) Scorer{
		"tier1": func(cfg Config) Scorer { return NewTier1(cfg).WithCrew(crew) },
		"tier2": func(cfg Config) Scorer { return NewTier2(cfg, saved).WithCrew(crew) },
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
			t.Errorf("%s: weight 0 should ignore crew, scores = %v", name, off)
		}
		cfg.CrewWeight = 0.5
		on := scores(cfg)
		if want := off[2] + 0.5*crew.Weight("movie", 525); math.Abs(on[2]-want) > 1e-9 {
			t.Errorf("%s: shared crew score = %v, want %v", name, on[2], want)
		}
		if on[3] != off[3] || on[4] != off[4] {
			t.Errorf("%s: titles sharing no crew moved: off %v, on %v", name, off, on)
		}
	}
}

type fakeCrew map[int][]int

func (f fakeCrew) RecoCrew(context.Context, []string) (map[int][]int, error) { return f, nil }

func TestLoadCrewIndex(t *testing.T) {
	x, err := LoadCrewIndex(context.Background(),
		fakeCrew{550: {7467, 7468}},
		fakeCrew{550: {66633}})
	if err != nil {
		t.Fatal(err)
	}
	if got := x.IDs("movie", 550); !reflect.DeepEqual(got, []int{7467, 7468}) {
		t.Errorf("film crew = %v", got)
	}
	if got := x.IDs("tv", 550); !reflect.DeepEqual(got, []int{66633}) {
		t.Errorf("series crew = %v, want its own, not the film's with the same id", got)
	}
}
