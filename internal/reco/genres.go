package reco

// tvCombinedGenres maps TMDB's combined TV genres onto the film genres they
// merge, so one taste profile covers films and series. Every other TV genre
// either shares its film id already (Drama is 18 in both) or has no film
// counterpart (Kids, News, Reality, Soap, Talk) and keeps its own id.
var tvCombinedGenres = map[int][]int{
	10759: {28, 12},  // Action & Adventure: Action, Adventure
	10765: {878, 14}, // Sci-Fi & Fantasy: Science Fiction, Fantasy
	10768: {10752},   // War & Politics: War
}

// CanonicalTVGenres returns a series' genre ids in the film genre space, in
// order, without duplicates. Callers read TV features through it so a series
// and a film that share a genre score as sharing it.
func CanonicalTVGenres(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int, 0, len(ids)+1)
	seen := make(map[int]bool, len(ids)+1)
	add := func(id int) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range ids {
		if film, ok := tvCombinedGenres[id]; ok {
			for _, f := range film {
				add(f)
			}
			continue
		}
		add(id)
	}
	return out
}

// NewTVFeatureIDF is NewFeatureIDF over the series feature stats, with their
// genre counts read into the film genre space the feed scores in.
func NewTVFeatureIDF(genreDF, keywordDF map[int]int, corpus int) *FeatureIDF {
	return NewFeatureIDF(canonicalTVGenreDF(genreDF), keywordDF, corpus)
}

// canonicalTVGenreDF reads series genre document frequencies, counted over
// TV's own ids, into the film genre space. A combined genre's count goes to
// each genre it splits into. Summing is exact because the cached series
// carry none of the film ids the combined genres split into.
func canonicalTVGenreDF(df map[int]int) map[int]int {
	out := make(map[int]int, len(df)+len(tvCombinedGenres))
	for id, n := range df {
		if film, ok := tvCombinedGenres[id]; ok {
			for _, f := range film {
				out[f] += n
			}
			continue
		}
		out[id] += n
	}
	return out
}
