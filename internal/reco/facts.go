package reco

import (
	"context"
	"fmt"
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

// FactIndex holds a set of ids for every catalog title and how rare each id
// is: the open-data facts, or the crew. Rarity is counted per media type over
// the titles that have any, like the keyword IDF, so a director with two
// films outweighs a city that is the setting of two hundred. A nil index
// holds nothing.
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

// CrewJobs are the TMDB crew jobs the crew term matches on: who directs,
// writes and scores a title. Producers and the rest of the shown crew are
// left out, since a studio producer spans too many unlike titles.
var CrewJobs = []string{
	"Director",
	"Screenplay", "Writer", "Story", "Novel", "Author",
	"Original Music Composer", "Music",
}

// sideIndexes are the indexes scored beside genres and keywords, each with its
// own weight in Config: the Wikidata facts, the crew, the related keywords,
// the random walk and the titles other users liked along with yours.
type sideIndexes struct {
	facts   *FactIndex
	crew    *FactIndex
	related *RelatedKeywords
	walk    *WalkGraph
	coLiked CoLikedSource
}

// sideWeights are the liked (or disliked) titles' pooled side ids.
type sideWeights struct {
	fact    map[int]float64
	crew    map[int]float64
	related map[int]float64
}

// pool sums the items' side ids. keywordW is the same items' pooled keyword
// weights, which the related keywords spread out from.
func (s sideIndexes) pool(items []SignalItem, keywordW map[int]float64) sideWeights {
	return sideWeights{
		fact:    pooledIndexWeights(items, s.facts, func(f *MediaFeatures) []int { return f.FactIDs }),
		crew:    pooledIndexWeights(items, s.crew, func(f *MediaFeatures) []int { return f.CrewIDs }),
		related: s.related.Expand(keywordW),
	}
}

// score is the side terms of a candidate's score. Each is 0 when its weight is
// 0 or the title shares none of its ids, so such titles score as before.
func (w sideWeights) score(cfg Config, f *MediaFeatures) float64 {
	var s float64
	if cfg.FactWeight != 0 {
		s += cfg.FactWeight * overlapScore(f.FactIDs, w.fact)
	}
	if cfg.CrewWeight != 0 {
		s += cfg.CrewWeight * overlapScore(f.CrewIDs, w.crew)
	}
	if cfg.RelatedKeywordWeight != 0 {
		s += cfg.RelatedKeywordWeight * overlapScore(f.KeywordIDs, w.related)
	}
	return s
}

// pooledIndexWeights sums the liked (or disliked) titles' signal per id, each
// scaled by the id's IDF in its own title's media type.
func pooledIndexWeights(items []SignalItem, x *FactIndex, ids func(*MediaFeatures) []int) map[int]float64 {
	out := map[int]float64{}
	for _, it := range items {
		if it.Features == nil {
			continue
		}
		for _, id := range ids(it.Features) {
			out[id] += it.Weight * x.Weight(it.Features.MediaType, id)
		}
	}
	return out
}

// CatalogSides are the per-title ids a fact catalog puts on its candidates'
// features. Any may be nil.
type CatalogSides struct {
	Facts *FactIndex
	Crew  *FactIndex
	Cast  *FactIndex
}

// factCatalog puts each title's open-data facts, crew and top-billed cast on
// the features of every candidate the catalog returns. It copies a title's
// features before adding them, since the catalog underneath may share them
// between requests.
type factCatalog struct {
	CatalogRepository
	sides CatalogSides
}

// NewFactCatalog wraps a catalog so its candidates carry their side ids.
func NewFactCatalog(catalog CatalogRepository, sides CatalogSides) CatalogRepository {
	return factCatalog{CatalogRepository: catalog, sides: sides}
}

func (c factCatalog) withFacts(cands []Candidate, err error) ([]Candidate, error) {
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, len(cands))
	for i, cand := range cands {
		facts := c.sides.Facts.IDs(cand.MediaType, cand.TMDBID)
		crew := c.sides.Crew.IDs(cand.MediaType, cand.TMDBID)
		cast := c.sides.Cast.IDs(cand.MediaType, cand.TMDBID)
		if (facts != nil || crew != nil || cast != nil) && cand.Features != nil {
			f := *cand.Features
			f.FactIDs = facts
			f.CrewIDs = crew
			f.CastIDs = cast
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

// CrewSource reads the crew ids of every cached title of one media type in the
// given jobs, keyed by TMDB id. The movies and series repositories are one.
type CrewSource interface {
	RecoCrew(ctx context.Context, jobs []string) (map[int][]int, error)
}

// LoadCrewIndex reads the films' and series' crew in CrewJobs into one index.
func LoadCrewIndex(ctx context.Context, films, series CrewSource) (*FactIndex, error) {
	var titles []TitleFacts
	for _, src := range []struct {
		mediaType string
		source    CrewSource
	}{{"movie", films}, {"tv", series}} {
		crew, err := src.source.RecoCrew(ctx, CrewJobs)
		if err != nil {
			return nil, fmt.Errorf("load %s crew: %w", src.mediaType, err)
		}
		for id, people := range crew {
			titles = append(titles, TitleFacts{MediaType: src.mediaType, TMDBID: id, IDs: people})
		}
	}
	return NewFactIndex(titles), nil
}
