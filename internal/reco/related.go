package reco

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
)

// The related-keyword index keeps, for each keyword, the keywords that most
// often tag the same titles. Two keywords are related when they share at
// least relatedMinCo titles and their cosine (shared titles over the root of
// each one's title count) is at least relatedMinSim. Each keyword keeps its
// relatedTopK closest.
const (
	relatedMinCo  = 3
	relatedMinSim = 0.1
	relatedTopK   = 10
)

// Related is one keyword near another, with how close it is (0 to 1].
type Related struct {
	ID  int
	Sim float64
}

// RelatedKeywords maps each keyword to the keywords that most often appear
// with it across the catalog, so a liked "heist" gives partial credit to
// "bank robbery". It is plain counting, not a learned weight. A nil index
// relates nothing.
type RelatedKeywords struct {
	near map[int][]Related
}

// NewRelatedKeywords counts which keywords tag the same titles. Each element
// of docs is one title's keyword ids. Films and series are counted together,
// since TMDB keywords are shared between them.
func NewRelatedKeywords(docs [][]int) *RelatedKeywords {
	df := map[int]int{}
	sets := make([][]int, 0, len(docs))
	for _, d := range docs {
		set := slices.Compact(slices.Sorted(slices.Values(d)))
		sets = append(sets, set)
		for _, k := range set {
			df[k]++
		}
	}

	// Only keywords on enough titles can share relatedMinCo of them.
	postings := map[int][]int{}
	for i, set := range sets {
		for _, k := range set {
			if df[k] >= relatedMinCo {
				postings[k] = append(postings[k], i)
			}
		}
	}

	near := map[int][]Related{}
	co := map[int]int{}
	for k, titles := range postings {
		clear(co)
		for _, i := range titles {
			for _, j := range sets[i] {
				if j != k && df[j] >= relatedMinCo {
					co[j]++
				}
			}
		}
		var list []Related
		for j, c := range co {
			if c < relatedMinCo {
				continue
			}
			if sim := float64(c) / math.Sqrt(float64(df[k])*float64(df[j])); sim >= relatedMinSim {
				list = append(list, Related{ID: j, Sim: sim})
			}
		}
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(a, b int) bool {
			if list[a].Sim != list[b].Sim {
				return list[a].Sim > list[b].Sim
			}
			return list[a].ID < list[b].ID
		})
		if len(list) > relatedTopK {
			list = list[:relatedTopK]
		}
		near[k] = slices.Clip(list)
	}
	return &RelatedKeywords{near: near}
}

// Near is the keywords related to id, closest first, or nil.
func (r *RelatedKeywords) Near(id int) []Related {
	if r == nil {
		return nil
	}
	return r.near[id]
}

// Len is how many keywords have at least one related keyword.
func (r *RelatedKeywords) Len() int {
	if r == nil {
		return 0
	}
	return len(r.near)
}

// Expand spreads pooled keyword weights to the related keywords, each scaled
// by how close it is. A keyword already in pooled gets no partial credit on
// top of its exact match. A nil index expands to nothing.
func (r *RelatedKeywords) Expand(pooled map[int]float64) map[int]float64 {
	if r == nil || len(pooled) == 0 {
		return nil
	}
	out := map[int]float64{}
	for k, w := range pooled {
		for _, n := range r.near[k] {
			if _, exact := pooled[n.ID]; exact {
				continue
			}
			out[n.ID] += w * n.Sim
		}
	}
	return out
}

// KeywordSource reads the keyword ids of every cached title of one media type
// that has a vote. The movies and series repositories are one.
type KeywordSource interface {
	RecoKeywordSets(ctx context.Context) ([][]int, error)
}

// LoadRelatedKeywords counts the keywords of every source's titles together.
func LoadRelatedKeywords(ctx context.Context, sources ...KeywordSource) (*RelatedKeywords, error) {
	var docs [][]int
	for _, src := range sources {
		sets, err := src.RecoKeywordSets(ctx)
		if err != nil {
			return nil, fmt.Errorf("load keyword sets: %w", err)
		}
		docs = append(docs, sets...)
	}
	return NewRelatedKeywords(docs), nil
}
