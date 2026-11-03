package reco

import "math"

// FeatureIDF supplies inverse-document-frequency multipliers for catalog
// features. pooledFeatureWeights sums interaction weights per feature, which
// lets features that recur across a large taste — genre Drama, keywords like
// "based on a true story" — accumulate dominant weight and collapse the feed
// toward the catalog centroid. Multiplying each feature's pooled weight by its
// IDF damps features that are common across the whole catalog and amplifies
// discriminative niche ones, so an eclectic taste's distinctive signal wins.
//
// The frequencies are global (over the catalog), not personal: personal
// frequency is already captured by the sum, so the remaining correction is for
// how informative a feature is in the catalog at large.
//
// A nil *FeatureIDF is the identity — every multiplier 1.0 — so a scorer built
// without stats (before feature_stats is populated, or in a unit test) behaves
// exactly as it did before IDF weighting existed.
type FeatureIDF struct {
	genre   map[int]float64
	keyword map[int]float64
}

// NewFeatureIDF builds an IDF table from per-feature document frequencies (df:
// how many catalog docs carry the feature) and the corpus size N. It uses the
// smoothed form idf = log((1+N)/(1+df)) + 1, which stays positive for every df
// (no feature is zeroed or flipped negative) and is well-defined at df == 0.
// Returns nil when the corpus is empty — the stats have not been computed yet —
// so callers transparently fall back to the identity.
func NewFeatureIDF(genreDF, keywordDF map[int]int, corpus int) *FeatureIDF {
	if corpus <= 0 {
		return nil
	}
	return &FeatureIDF{
		genre:   idfMap(genreDF, corpus),
		keyword: idfMap(keywordDF, corpus),
	}
}

func idfMap(df map[int]int, corpus int) map[int]float64 {
	out := make(map[int]float64, len(df))
	n := float64(corpus)
	for id, d := range df {
		out[id] = math.Log((1+n)/(1+float64(d))) + 1
	}
	return out
}

// GenreWeight returns the IDF multiplier for a genre id. A nil receiver and an
// unknown id both return 1.0: an unknown feature carries no candidate in the
// pool (candidates are drawn from the same catalog the stats were computed
// over), so its multiplier never actually reaches a score.
func (f *FeatureIDF) GenreWeight(id int) float64 {
	if f == nil {
		return 1
	}
	if w, ok := f.genre[id]; ok {
		return w
	}
	return 1
}

// KeywordWeight is the keyword twin of GenreWeight.
func (f *FeatureIDF) KeywordWeight(id int) float64 {
	if f == nil {
		return 1
	}
	if w, ok := f.keyword[id]; ok {
		return w
	}
	return 1
}

// MediaIDF holds one FeatureIDF per media type. Films and series are counted
// over their own catalogs, because a keyword's rarity among films says little
// about its rarity among series. A nil MediaIDF, or a nil table for one type,
// weighs that type's features the same.
type MediaIDF struct {
	Movie *FeatureIDF
	// TV is read in the film genre space (see NewTVFeatureIDF).
	TV *FeatureIDF
}

// For returns the table for a media type: TV's for "tv", the film table for
// anything else.
func (m *MediaIDF) For(mediaType string) *FeatureIDF {
	if m == nil {
		return nil
	}
	if mediaType == "tv" {
		return m.TV
	}
	return m.Movie
}
