package rankings

import (
	"cmp"
	"slices"

	"klyvi-api/internal/toprated"
)

// weigh drops what the top lists leave out (too few votes, and Talk, News,
// Reality and Soap on TV) and orders the rest by toprated.Weighted, highest
// first. TMDB filters the same way, this holds even if it lets one by.
func weigh(results []map[string]any, kind mediaKind) []map[string]any {
	floor := toprated.MovieVoteFloor
	if kind == mediaTV {
		floor = toprated.TVVoteFloor
	}

	pool := make([]map[string]any, 0, len(results))
	for _, r := range results {
		if intField(r, "vote_count") < floor {
			continue
		}
		if kind == mediaTV && toprated.ExcludedTV(genreIDs(r)) {
			continue
		}
		pool = append(pool, r)
	}

	score := func(r map[string]any) float64 {
		return toprated.Weighted(floatField(r, "vote_average"), intField(r, "vote_count"), floor, toprated.CatalogMean)
	}
	slices.SortStableFunc(pool, func(a, b map[string]any) int {
		return cmp.Compare(score(b), score(a))
	})
	return pool
}

func genreIDs(r map[string]any) []int {
	raw, _ := r["genre_ids"].([]any)
	ids := make([]int, 0, len(raw))
	for _, g := range raw {
		if f, ok := g.(float64); ok {
			ids = append(ids, int(f))
		}
	}
	return ids
}
