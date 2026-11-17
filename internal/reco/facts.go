package reco

import (
	"context"
	"math"
)

// TitleFacts are the open-data facts one catalog title is matched on: the
// Wikidata items for its directors, main subjects, narrative locations and
// source material, in one list. They come from Wikidata (CC0) by TMDB id, so
// no TMDB text ever reaches this term.
type TitleFacts struct {
	MediaType string
	TMDBID    int
	IDs       []int
}

type factKey struct {
	mediaType string
	tmdbID    int
}

// FactIndex holds every catalog title's open-data facts and how rare each fact
// is. Rarity is counted per media type over the titles Wikidata matched, like
// the keyword IDF, so a director with two films outweighs a city that is the
// setting of two hundred. A nil index holds no facts.
type FactIndex struct {
	ids map[factKey][]int
	idf map[string]map[int]float64
}

// NewFactIndex indexes the titles' facts and their IDF, the same smoothed
// log((1+N)/(1+df)) + 1 the keyword tables use.
func NewFactIndex(titles []TitleFacts) *FactIndex {
	x := &FactIndex{ids: make(map[factKey][]int, len(titles)), idf: map[string]map[int]float64{}}
	df := map[string]map[int]int{}
	corpus := map[string]int{}
	for _, t := range titles {
		if len(t.IDs) == 0 {
			continue
		}
		x.ids[factKey{t.MediaType, t.TMDBID}] = t.IDs
		corpus[t.MediaType]++
		if df[t.MediaType] == nil {
			df[t.MediaType] = map[int]int{}
		}
		for _, id := range t.IDs {
			df[t.MediaType][id]++
		}
	}
	for mt, counts := range df {
		n := float64(corpus[mt])
		w := make(map[int]float64, len(counts))
		for id, d := range counts {
			w[id] = math.Log((1+n)/(1+float64(d))) + 1
		}
		x.idf[mt] = w
	}
	return x
}

// Len is how many titles hold facts.
func (x *FactIndex) Len() int {
	if x == nil {
		return 0
	}
	return len(x.ids)
}

// IDs are a title's facts, nil when Wikidata did not match it.
func (x *FactIndex) IDs(mediaType string, tmdbID int) []int {
	if x == nil {
		return nil
	}
	return x.ids[factKey{mediaType, tmdbID}]
}

// Weight is a fact's IDF among titles of the media type, 1 for a fact the
// index does not hold.
func (x *FactIndex) Weight(mediaType string, id int) float64 {
	if x == nil {
		return 1
	}
	if w, ok := x.idf[mediaType][id]; ok {
		return w
	}
	return 1
}

// pooledFactWeights sums the liked (or disliked) titles' signal per fact,
// each scaled by the fact's IDF in its own title's media type.
func pooledFactWeights(items []SignalItem, x *FactIndex) map[int]float64 {
	out := map[int]float64{}
	for _, it := range items {
		if it.Features == nil {
			continue
		}
		for _, id := range it.Features.FactIDs {
			out[id] += it.Weight * x.Weight(it.Features.MediaType, id)
		}
	}
	return out
}

// factCatalog puts each title's open-data facts on the features of every
// candidate the catalog returns. It copies a title's features before adding
// them, since the catalog underneath may share them between requests.
type factCatalog struct {
	CatalogRepository
	facts *FactIndex
}

// NewFactCatalog wraps a catalog so its candidates carry their facts.
func NewFactCatalog(catalog CatalogRepository, facts *FactIndex) CatalogRepository {
	return factCatalog{CatalogRepository: catalog, facts: facts}
}

func (c factCatalog) withFacts(cands []Candidate, err error) ([]Candidate, error) {
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, len(cands))
	for i, cand := range cands {
		if ids := c.facts.IDs(cand.MediaType, cand.TMDBID); ids != nil && cand.Features != nil {
			f := *cand.Features
			f.FactIDs = ids
			cand.Features = &f
		}
		out[i] = cand
	}
	return out, nil
}

func (c factCatalog) SampleMovieCandidates(ctx context.Context, limit int) ([]Candidate, error) {
	return c.withFacts(c.CatalogRepository.SampleMovieCandidates(ctx, limit))
}

func (c factCatalog) SampleSeriesCandidates(ctx context.Context, limit int) ([]Candidate, error) {
	return c.withFacts(c.CatalogRepository.SampleSeriesCandidates(ctx, limit))
}

func (c factCatalog) CandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]Candidate, error) {
	return c.withFacts(c.CatalogRepository.CandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit))
}

func (c factCatalog) SeriesCandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]Candidate, error) {
	return c.withFacts(c.CatalogRepository.SeriesCandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit))
}

func (c factCatalog) CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]Candidate, error) {
	return c.withFacts(c.CatalogRepository.CandidatesByMediaIDs(ctx, mediaIDs))
}
