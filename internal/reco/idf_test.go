package reco

import (
	"math"
	"testing"

	"github.com/google/uuid"
)

// A ubiquitous feature (df close to N) must get a much smaller IDF multiplier
// than a rare one (small df). This is the whole point: it damps the features
// that would otherwise dominate a diverse taste.
func TestFeatureIDF_RareBeatsCommon(t *testing.T) {
	const corpus = 10000
	genreDF := map[int]int{
		18: 4000, // Drama — everywhere
		99: 40,   // Documentary — niche
	}
	idf := NewFeatureIDF(genreDF, nil, corpus)

	drama := idf.GenreWeight(18)
	doc := idf.GenreWeight(99)

	if !(doc > drama) {
		t.Fatalf("rare feature should outweigh common: doc=%.3f drama=%.3f", doc, drama)
	}
	// Smoothed idf = log((1+N)/(1+df)) + 1.
	wantDrama := math.Log(float64(1+corpus)/float64(1+4000)) + 1
	if math.Abs(drama-wantDrama) > 1e-9 {
		t.Errorf("drama idf: got %.6f, want %.6f", drama, wantDrama)
	}
	// Every multiplier stays strictly positive — no feature is zeroed or flipped.
	if drama <= 0 {
		t.Errorf("idf must stay positive, got %.6f", drama)
	}
}

// df == 0 (a feature absent from the corpus) must be well-defined and maximal,
// not a divide-by-zero or NaN.
func TestFeatureIDF_ZeroDFIsFiniteAndMax(t *testing.T) {
	const corpus = 10000
	idf := NewFeatureIDF(map[int]int{7: 0, 8: 1}, nil, corpus)
	zero := idf.GenreWeight(7)
	one := idf.GenreWeight(8)
	if math.IsNaN(zero) || math.IsInf(zero, 0) {
		t.Fatalf("df=0 idf not finite: %v", zero)
	}
	if !(zero > one) {
		t.Errorf("df=0 should be the largest idf: zero=%.3f one=%.3f", zero, one)
	}
}

// A nil *FeatureIDF and an unknown feature id both act as identity (1.0), so
// scoring without stats is exactly the old summing behaviour.
func TestFeatureIDF_NilAndUnknownAreIdentity(t *testing.T) {
	var nilIDF *FeatureIDF
	if got := nilIDF.GenreWeight(18); got != 1 {
		t.Errorf("nil idf genre: got %.3f, want 1", got)
	}
	if got := nilIDF.KeywordWeight(9826); got != 1 {
		t.Errorf("nil idf keyword: got %.3f, want 1", got)
	}

	idf := NewFeatureIDF(map[int]int{18: 100}, nil, 1000)
	if got := idf.GenreWeight(4242); got != 1 {
		t.Errorf("unknown genre id: got %.3f, want 1", got)
	}
}

// An empty corpus yields a nil table (identity) so the app runs unchanged
// before feature_stats has ever been refreshed.
func TestNewFeatureIDF_EmptyCorpusIsNil(t *testing.T) {
	if idf := NewFeatureIDF(map[int]int{18: 3}, map[int]int{1: 1}, 0); idf != nil {
		t.Errorf("empty corpus should give nil (identity) idf, got %+v", idf)
	}
}

// The identity idf must reproduce the raw summed pooled weights exactly — the
// guarantee that adding IDF plumbing does not perturb the pre-IDF pipeline.
func TestPooledFeatureWeights_IdentityMatchesRawSum(t *testing.T) {
	liked := []SignalItem{
		{MediaID: 1, Weight: 2.0, Features: &MediaFeatures{GenreIDs: []int{18, 80}}},
		{MediaID: 2, Weight: 1.5, Features: &MediaFeatures{GenreIDs: []int{18}}},
	}
	var nilIDF *FeatureIDF
	got := pooledFeatureWeights(liked, genreKeyset, nilIDF.GenreWeight)

	want := map[int]float64{18: 3.5, 80: 2.0}
	for id, w := range want {
		if math.Abs(got[id]-w) > 1e-9 {
			t.Errorf("genre %d: got %.3f, want %.3f", id, got[id], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("weight map size: got %d, want %d", len(got), len(want))
	}
}

// IDF must scale a feature's pooled weight by exactly its multiplier — pooling
// then damping equals damping each contribution.
func TestPooledFeatureWeights_AppliesIDF(t *testing.T) {
	const corpus = 10000
	df := 100
	idf := NewFeatureIDF(map[int]int{18: df}, nil, corpus)

	liked := []SignalItem{
		{MediaID: 1, Weight: 2.0, Features: &MediaFeatures{GenreIDs: []int{18}}},
		{MediaID: 2, Weight: 1.0, Features: &MediaFeatures{GenreIDs: []int{18}}},
	}
	got := pooledFeatureWeights(liked, genreKeyset, idf.GenreWeight)

	mult := math.Log(float64(1+corpus)/float64(1+df)) + 1
	want := 3.0 * mult // (2.0 + 1.0) summed, then scaled
	if math.Abs(got[18]-want) > 1e-9 {
		t.Errorf("idf-weighted pool: got %.6f, want %.6f", got[18], want)
	}
}

// BuildProfile with identity idf yields the same weights as the pre-IDF code
// path, so Tier 2's persisted profile is unchanged until stats are wired in.
func TestBuildProfile_IdentityIDFUnchanged(t *testing.T) {
	liked := []SignalItem{
		{MediaID: 1, Weight: 1.0, Features: &MediaFeatures{GenreIDs: []int{18}, KeywordIDs: []int{500}, Year: 2003}},
	}
	p := BuildProfile(uuid.New(), liked, nil, nil)
	if math.Abs(p.GenreWeights[18]-1.0) > 1e-9 {
		t.Errorf("genre weight: got %.3f, want 1.0", p.GenreWeights[18])
	}
	if math.Abs(p.KeywordWeights[500]-1.0) > 1e-9 {
		t.Errorf("keyword weight: got %.3f, want 1.0", p.KeywordWeights[500])
	}
}
