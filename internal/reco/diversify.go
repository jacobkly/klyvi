package reco

import "slices"

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
	for i := range scored {
		feats[i] = newFeatureSet(scored[i].Features)
	}
	maxSim := make([]float64, len(scored))
	taken := make([]bool, len(scored))

	selected := make([]Scored, 0, k)
	pick := func(idx int) {
		taken[idx] = true
		selected = append(selected, scored[idx])
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

	// First pick: highest score (input is already sorted by Score desc when
	// MMRReorder is called by the orchestrator, but we don't rely on that).
	bestIdx := 0
	for i := 1; i < len(scored); i++ {
		if scored[i].Score > scored[bestIdx].Score {
			bestIdx = i
		}
	}
	pick(bestIdx)

	for len(selected) < k {
		// Unpicked candidates are scanned in input order, so ties still go
		// to the earlier candidate.
		nextIdx := -1
		bestMMR := -1e18
		for i := range scored {
			if taken[i] {
				continue
			}
			mmr := lambda*scored[i].Score - (1-lambda)*maxSim[i]
			if mmr > bestMMR {
				bestMMR = mmr
				nextIdx = i
			}
		}
		if nextIdx < 0 {
			break
		}
		pick(nextIdx)
	}

	return selected
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
