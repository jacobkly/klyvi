package reco

import (
	"context"
	"fmt"
	"math"
	"slices"
)

// CastTop is how many of a title's top-billed cast the walk steps through.
// Further down the credits are bit parts that link unlike titles.
const CastTop = 8

// walkNode is one step of the walk between titles: a keyword or a cast member.
// The two share an id space in TMDB's numbering only by accident, so the kind
// keeps them apart.
type walkNode struct {
	cast bool
	id   int
}

// WalkGraph counts how many titles carry each keyword and cast member, the
// node degrees of a random walk from a user's liked titles through what they
// share to other titles (RP3beta). A rare node passes on more of the walk
// than a common one. A nil graph counts every node once.
type WalkGraph struct {
	degree map[walkNode]int
}

// NewWalkGraph counts the node degrees over every title's keywords and cast.
// Each element of keywordSets and castSets is one title's ids.
func NewWalkGraph(keywordSets, castSets [][]int) *WalkGraph {
	g := &WalkGraph{degree: map[walkNode]int{}}
	count := func(sets [][]int, cast bool) {
		for _, set := range sets {
			for _, id := range slices.Compact(slices.Sorted(slices.Values(set))) {
				g.degree[walkNode{cast, id}]++
			}
		}
	}
	count(keywordSets, false)
	count(castSets, true)
	return g
}

// Len is how many nodes the graph holds.
func (g *WalkGraph) Len() int {
	if g == nil {
		return 0
	}
	return len(g.degree)
}

func (g *WalkGraph) deg(n walkNode) float64 {
	if g == nil {
		return 1
	}
	return math.Max(float64(g.degree[n]), 1)
}

func walkNodes(f *MediaFeatures) []walkNode {
	nodes := make([]walkNode, 0, len(f.KeywordIDs)+len(f.CastIDs))
	for _, id := range f.KeywordIDs {
		nodes = append(nodes, walkNode{false, id})
	}
	for _, id := range f.CastIDs {
		nodes = append(nodes, walkNode{true, id})
	}
	return nodes
}

// Scores walks three steps from the liked titles: to each liked title's
// keywords and cast evenly, on to every candidate that shares one (split by
// how many titles carry it), then divides each candidate's share by its vote
// count to the power beta, so the walk does not just find the famous titles.
// The scores are scaled so the best candidate is 1, one per candidate in
// order. A candidate the walk does not reach scores 0.
func (g *WalkGraph) Scores(liked []SignalItem, cands []Candidate, beta float64) []float64 {
	reach := map[walkNode]float64{}
	for _, it := range liked {
		if it.Features == nil || it.Weight <= 0 {
			continue
		}
		nodes := walkNodes(it.Features)
		for _, n := range nodes {
			reach[n] += it.Weight / float64(len(nodes))
		}
	}

	out := make([]float64, len(cands))
	if len(reach) == 0 {
		return out
	}
	var top float64
	for i, c := range cands {
		if c.Features == nil {
			continue
		}
		var s float64
		for _, n := range walkNodes(c.Features) {
			if p, ok := reach[n]; ok {
				s += p / g.deg(n)
			}
		}
		if s > 0 && beta != 0 {
			s /= math.Pow(math.Max(float64(c.Features.VoteCount), 1), beta)
		}
		out[i] = s
		top = math.Max(top, s)
	}
	if top > 0 {
		for i := range out {
			out[i] /= top
		}
	}
	return out
}

// walkScores is the walk term for each candidate, or nil when it is off.
func (s sideIndexes) walkScores(cfg Config, liked []SignalItem, cands []Candidate) []float64 {
	if cfg.WalkWeight == 0 {
		return nil
	}
	return s.walk.Scores(liked, cands, cfg.WalkBeta)
}

// Sets are every indexed title's ids, one slice per title.
func (x *FactIndex) Sets() [][]int {
	if x == nil {
		return nil
	}
	out := make([][]int, 0, len(x.ids))
	for _, ids := range x.ids {
		out = append(out, ids)
	}
	return out
}

// CastSource reads the TMDB person ids of the first top billed cast of every
// cached title of one media type, keyed by TMDB id. The movies and series
// repositories are one.
type CastSource interface {
	RecoCast(ctx context.Context, top int) (map[int][]int, error)
}

// LoadCastIndex reads the films' and series' top CastTop billed cast into one
// index.
func LoadCastIndex(ctx context.Context, films, series CastSource) (*FactIndex, error) {
	var titles []TitleFacts
	for _, src := range []struct {
		mediaType string
		source    CastSource
	}{{"movie", films}, {"tv", series}} {
		cast, err := src.source.RecoCast(ctx, CastTop)
		if err != nil {
			return nil, fmt.Errorf("load %s cast: %w", src.mediaType, err)
		}
		for id, people := range cast {
			titles = append(titles, TitleFacts{MediaType: src.mediaType, TMDBID: id, IDs: people})
		}
	}
	return NewFactIndex(titles), nil
}
