// Package toprated holds the one rule for what counts as top rated, shared by
// the Top 100 rankings and the movie and TV "top_rated" browse lists.
//
// TMDB's own top_rated list ranks a 9.0 with 40 votes above The Godfather.
// Here a title needs a floor of votes to be listed at all, and Top 100
// re-ranks its pool by IMDb's weighted score so a big audience counts.
package toprated

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MovieVoteFloor is the fewest votes a film needs. 2,000 on TMDB means a film
// most people have heard of. The Godfather and Shawshank have over 20,000.
const MovieVoteFloor = 2000

// TVVoteFloor is the fewest votes a show needs. TV gets about a quarter of the
// votes films do, so 500 keeps good shows with a smaller audience.
const TVVoteFloor = 500

// CatalogMean is C in Weighted: about the average score across TMDB, as IMDb
// uses its whole catalogue's mean. The mean of the top-rated pool itself sits
// near 8.3, too high to pull a new title with a thousand votes back down.
const CatalogMean = 7.0

// excludedTVGenres crowd TV lists without being what people mean by top rated:
// Talk, News, Reality and Soap.
var excludedTVGenres = []int{10767, 10763, 10764, 10766}

// MoviePath is page n of the films above the floor, best score first.
func MoviePath(page int) string {
	return fmt.Sprintf("/discover/movie?include_adult=false&language=en-US&page=%d&sort_by=vote_average.desc&vote_count.gte=%d",
		page, MovieVoteFloor)
}

// TVPath is page n of the shows above the floor, best score first, without
// the genres in excludedTVGenres.
func TVPath(page int) string {
	ids := make([]string, len(excludedTVGenres))
	for i, g := range excludedTVGenres {
		ids[i] = strconv.Itoa(g)
	}
	return fmt.Sprintf("/discover/tv?include_adult=false&language=en-US&page=%d&sort_by=vote_average.desc&vote_count.gte=%d&without_genres=%s",
		page, TVVoteFloor, strings.Join(ids, ","))
}

// ExcludedTV reports whether a show with these genre ids is left out of TV
// lists. TMDB's filter is applied too, this catches anything it lets by.
func ExcludedTV(genreIDs []int) bool {
	for _, g := range genreIDs {
		if slices.Contains(excludedTVGenres, g) {
			return true
		}
	}
	return false
}

// Weighted is IMDb's weighted score, (v / (v + m)) × R + (m / (v + m)) × C:
// R is the title's average, v its vote count, m the vote floor and C the
// catalogue's mean (CatalogMean). A title near the floor is pulled toward the mean, and one with
// many votes keeps nearly all of its own score.
func Weighted(r float64, v, m int, c float64) float64 {
	if v+m == 0 {
		return c
	}
	vf, mf := float64(v), float64(m)
	return vf/(vf+mf)*r + mf/(vf+mf)*c
}
