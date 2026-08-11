package reco

import (
	"context"
	"sort"
)

// maxTasteKeywords caps how many keyword ids the taste-overlap retrieval uses.
// A user with a large history can accumulate over a thousand distinct keywords;
// passing them all to the overlap query is slow and dilutes retrieval with
// one-off tags. Keeping the most-shared keywords focuses on core taste and
// bounds query cost. Generous enough that a typical taste is unaffected.
const maxTasteKeywords = 300

// candidateSource is the subset of the catalog that pool assembly needs —
// the two retrieval methods, without the reason-name lookup the harness has
// no use for. Kept minimal so both the orchestrator's full CatalogRepository
// and the harness's lighter adapter satisfy it.
type candidateSource interface {
	SampleMovieCandidates(ctx context.Context, limit int) ([]Candidate, error)
	CandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]Candidate, error)
}

// BuildCandidatePool assembles the fixed-size pool a scorer ranks over.
//
// For a user with signal it retrieves candidates that share the taste's
// genres/keywords (so the feed is drawn from taste-relevant titles, not the
// globally most-popular ones), then tops up with the popularity pool if that
// retrieval is thin — a user with one liked item should not get a three-item
// feed. A cold user (no resolved liked features) gets the popularity pool
// directly, which is exactly what Tier 0 expects.
//
// It lives here, not inline in the orchestrator, so the evaluation harness
// builds its pool the same way — otherwise the harness would measure a
// different pipeline than production runs.
func BuildCandidatePool(ctx context.Context, catalog candidateSource, liked []SignalItem, cfg Config) ([]Candidate, error) {
	genreIDs, keywordIDs := likedFeatureIDs(liked)

	var cands []Candidate
	if len(genreIDs) > 0 || len(keywordIDs) > 0 {
		var err error
		cands, err = catalog.CandidatesByTaste(ctx, genreIDs, keywordIDs, cfg.QualityFloorVotes, candidatePoolSize)
		if err != nil {
			return nil, err
		}
	}

	if len(cands) < candidatePoolSize {
		pop, err := catalog.SampleMovieCandidates(ctx, candidatePoolSize)
		if err != nil {
			return nil, err
		}
		cands = mergeCandidates(cands, pop, candidatePoolSize)
	}

	return cands, nil
}

// likedFeatureIDs collects the taste to retrieve candidates for: the distinct
// genre ids, and the most-shared keyword ids (capped at maxTasteKeywords).
// Keywords are ranked by how many liked items carry them, so the core taste
// wins the cap and one-off tags fall away.
func likedFeatureIDs(liked []SignalItem) (genreIDs, keywordIDs []int) {
	gSeen := map[int]bool{}
	kFreq := map[int]int{}
	for _, it := range liked {
		if it.Features == nil {
			continue
		}
		for _, g := range it.Features.GenreIDs {
			if !gSeen[g] {
				gSeen[g] = true
				genreIDs = append(genreIDs, g)
			}
		}
		for _, k := range it.Features.KeywordIDs {
			kFreq[k]++
		}
	}
	return genreIDs, topKeywordsByFreq(kFreq, maxTasteKeywords)
}

// topKeywordsByFreq returns up to n keyword ids, most-shared first, with id as
// a deterministic tiebreak so the pool (and thus pagination) stays stable.
func topKeywordsByFreq(freq map[int]int, n int) []int {
	type kf struct{ id, count int }
	all := make([]kf, 0, len(freq))
	for id, c := range freq {
		all = append(all, kf{id, c})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].count != all[j].count {
			return all[i].count > all[j].count
		}
		return all[i].id < all[j].id
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make([]int, len(all))
	for i, x := range all {
		out[i] = x.id
	}
	return out
}

// mergeCandidates concatenates primary then secondary, dropping duplicate
// media ids and stopping at limit. Primary keeps its order and priority.
func mergeCandidates(primary, secondary []Candidate, limit int) []Candidate {
	seen := make(map[int]bool, len(primary))
	out := make([]Candidate, 0, limit)
	add := func(cs []Candidate) {
		for _, c := range cs {
			if len(out) >= limit {
				return
			}
			if !seen[c.MediaID] {
				seen[c.MediaID] = true
				out = append(out, c)
			}
		}
	}
	add(primary)
	add(secondary)
	return out
}
