package reco

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

	selected := make([]Scored, 0, k)
	remaining := make([]Scored, len(scored))
	copy(remaining, scored)

	// First pick: highest score (input is already sorted by Score desc when
	// MMRReorder is called by the orchestrator, but we don't rely on that).
	bestIdx := 0
	for i := 1; i < len(remaining); i++ {
		if remaining[i].Score > remaining[bestIdx].Score {
			bestIdx = i
		}
	}
	selected = append(selected, remaining[bestIdx])
	remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)

	for len(selected) < k && len(remaining) > 0 {
		nextIdx := -1
		bestMMR := -1e18
		for i, c := range remaining {
			var maxSim float64
			for _, s := range selected {
				if sim := candidateSimilarity(c, s); sim > maxSim {
					maxSim = sim
				}
			}
			mmr := lambda*c.Score - (1-lambda)*maxSim
			if mmr > bestMMR {
				bestMMR = mmr
				nextIdx = i
			}
		}
		if nextIdx < 0 {
			break
		}
		selected = append(selected, remaining[nextIdx])
		remaining = append(remaining[:nextIdx], remaining[nextIdx+1:]...)
	}

	return selected
}

// candidateSimilarity is a Jaccard-style overlap over the union of genre
// and keyword feature ids. Returns 0 when either candidate has no features
// (e.g. Tier 0 raw candidates).
func candidateSimilarity(a, b Scored) float64 {
	if a.Features == nil || b.Features == nil {
		return 0
	}
	aSet := make(map[int]bool, len(a.Features.GenreIDs)+len(a.Features.KeywordIDs))
	for _, g := range a.Features.GenreIDs {
		aSet[encodeGenre(g)] = true
	}
	for _, k := range a.Features.KeywordIDs {
		aSet[encodeKeyword(k)] = true
	}

	bSize := len(b.Features.GenreIDs) + len(b.Features.KeywordIDs)
	if bSize == 0 || len(aSet) == 0 {
		return 0
	}
	var inter int
	for _, g := range b.Features.GenreIDs {
		if aSet[encodeGenre(g)] {
			inter++
		}
	}
	for _, k := range b.Features.KeywordIDs {
		if aSet[encodeKeyword(k)] {
			inter++
		}
	}
	// Symmetric measure: |A ∩ B| / min(|A|, |B|) — emphasizes overlap
	// regardless of catalog noise from richly-tagged items.
	minSize := len(aSet)
	if bSize < minSize {
		minSize = bSize
	}
	return float64(inter) / float64(minSize)
}

// Genre and keyword id namespaces collide; encode each into its own slot
// so genre id 14 never matches keyword id 14 in the similarity set.
func encodeGenre(id int) int   { return id }
func encodeKeyword(id int) int { return -id - 1 }
