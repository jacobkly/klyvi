package reco

import (
	"math"
	"slices"
)

// TypeBound is the most of a feed each media type may fill, as a share from
// 0 to 1 of every stretch of the feed from the top.
type TypeBound struct {
	TV    float64
	Movie float64
}

// FeedTypeBound keeps either type from taking over a feed. Series may fill
// cfg.TVShare of it and films the rest, unless the user's own history leans
// further one way, in which case that type gets the history's share. A
// film-only user sees at most 4 shows in 10, and a seasons-only user can get
// a feed of shows with at most 6 films in 10. Liked items whose features did
// not resolve have no known type and do not count.
func FeedTypeBound(cfg Config, liked []SignalItem) TypeBound {
	share := min(max(cfg.TVShare, 0), 1)
	var tv, known int
	for _, li := range liked {
		if li.Features == nil {
			continue
		}
		known++
		if li.Features.MediaType == "tv" {
			tv++
		}
	}
	historyTV := 0.0
	if known > 0 {
		historyTV = float64(tv) / float64(known)
	}
	return TypeBound{TV: max(share, historyTV), Movie: max(1-share, 1-historyTV)}
}

// MMRReorder rearranges scored candidates to balance relevance with novelty
// against items already picked. λ controls the balance:
//
//	λ=1.0 → pure relevance, no diversification
//	λ=0.0 → pure novelty
//
// At λ around 0.7 (the default), the feed prefers high-scoring items but
// will pass over the next high-scoring item if it duplicates the genre and
// keywords of something already shown — the difference between a smart-
// looking feed and five films from the same franchise.
func MMRReorder(scored []Scored, k int, lambda float64) []Scored {
	return MMRReorderWithin(scored, k, lambda, TypeBound{TV: 1, Movie: 1})
}

// MMRReorderWithin is MMRReorder with each media type held to its share of
// the feed. At every position a type may fill at most its share of the picks
// so far, rounded up, so the top item can be either type. Once only one type
// has room or candidates left it takes the next pick, so the bound orders the
// feed and never shortens it. Each pick depends only on the picks before it,
// so the order stays prefix-stable.
func MMRReorderWithin(scored []Scored, k int, lambda float64, bound TypeBound) []Scored {
	if len(scored) == 0 || k <= 0 {
		return nil
	}
	if k > len(scored) {
		k = len(scored)
	}

	// Each candidate's features are sorted once, and each carries its running
	// max similarity to the picks so far. A new pick only has to be compared
	// against the unpicked candidates, never the whole selected list again.
	feats := make([]featureSet, len(scored))
	isTV := make([]bool, len(scored))
	for i := range scored {
		feats[i] = newFeatureSet(scored[i].Features)
		isTV[i] = scored[i].MediaType == "tv"
	}
	maxSim := make([]float64, len(scored))
	taken := make([]bool, len(scored))

	selected := make([]Scored, 0, k)
	var tvPicked, moviePicked int
	pick := func(idx int) {
		taken[idx] = true
		selected = append(selected, scored[idx])
		if isTV[idx] {
			tvPicked++
		} else {
			moviePicked++
		}
		if len(selected) == k {
			return
		}
		for i := range scored {
			if taken[i] {
				continue
			}
			if sim := feats[i].similarity(feats[idx]); sim > maxSim[i] {
				maxSim[i] = sim
			}
		}
	}

	// room reports whether the next pick may be candidate i's type.
	room := func(i int) bool {
		n := len(selected) + 1
		if isTV[i] {
			return tvPicked < shareOf(bound.TV, n)
		}
		return moviePicked < shareOf(bound.Movie, n)
	}

	// First pick: highest score among the types with room (input is already
	// sorted by Score desc when MMRReorder is called by the orchestrator, but
	// we don't rely on that), else the highest score.
	bestIdx, fallback := -1, -1
	for i := range scored {
		if fallback < 0 || scored[i].Score > scored[fallback].Score {
			fallback = i
		}
		if room(i) && (bestIdx < 0 || scored[i].Score > scored[bestIdx].Score) {
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		bestIdx = fallback
	}
	pick(bestIdx)

	for len(selected) < k {
		// Unpicked candidates are scanned in input order, so ties still go
		// to the earlier candidate.
		nextIdx, fallback := -1, -1
		bestMMR, fallbackMMR := -1e18, -1e18
		for i := range scored {
			if taken[i] {
				continue
			}
			mmr := lambda*scored[i].Score - (1-lambda)*maxSim[i]
			if mmr > fallbackMMR {
				fallbackMMR = mmr
				fallback = i
			}
			if mmr > bestMMR && room(i) {
				bestMMR = mmr
				nextIdx = i
			}
		}
		if nextIdx < 0 {
			nextIdx = fallback
		}
		if nextIdx < 0 {
			break
		}
		pick(nextIdx)
	}

	return selected
}

// shareOf is the most of the first n picks a type with the given share may
// hold, rounded up. The slack keeps a share that lands on a whole number,
// like 0.4 of 5, from rounding past it on float error.
func shareOf(share float64, n int) int {
	return int(math.Ceil(share*float64(n) - 1e-9))
}

// featureSet is a candidate's genre and keyword ids in one sorted, encoded
// id space, built once per MMR pass so comparisons never allocate.
type featureSet struct {
	ids      []int // every encoded id, sorted, repeats kept
	distinct int   // number of distinct ids
}

func newFeatureSet(f *MediaFeatures) featureSet {
	if f == nil {
		return featureSet{}
	}
	ids := make([]int, 0, len(f.GenreIDs)+len(f.KeywordIDs))
	for _, g := range f.GenreIDs {
		ids = append(ids, encodeGenre(g))
	}
	for _, k := range f.KeywordIDs {
		ids = append(ids, encodeKeyword(k))
	}
	slices.Sort(ids)
	distinct := 0
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			distinct++
		}
	}
	return featureSet{ids: ids, distinct: distinct}
}

// similarity is the overlap |A ∩ B| / min(|A|, |B|) over genre and keyword
// ids, where a is the candidate and b the already-picked item. It emphasizes
// overlap regardless of catalog noise from richly-tagged items. It is 0 when
// either side has no features (e.g. Tier 0 raw candidates).
func (a featureSet) similarity(b featureSet) float64 {
	if len(b.ids) == 0 || a.distinct == 0 {
		return 0
	}
	// Every id b carries that a also has counts, repeats included.
	var inter, i, j int
	for i < len(a.ids) && j < len(b.ids) {
		switch {
		case a.ids[i] < b.ids[j]:
			i++
		case a.ids[i] > b.ids[j]:
			j++
		default:
			inter++
			j++
		}
	}
	return float64(inter) / float64(min(a.distinct, len(b.ids)))
}

// Genre and keyword id namespaces collide; encode each into its own slot
// so genre id 14 never matches keyword id 14 in the similarity set.
func encodeGenre(id int) int   { return id }
func encodeKeyword(id int) int { return -id - 1 }
