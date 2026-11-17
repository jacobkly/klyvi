package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/jmoiron/sqlx"
)

// ndcgAt is NDCG@k for a trial with one relevant item at a 1-based rank, 0
// a miss. The ideal ranking puts it first, so the ideal DCG is 1.
func ndcgAt(rank, k int) float64 {
	if rank <= 0 || rank > k {
		return 0
	}
	return 1 / math.Log2(float64(rank)+1)
}

// titleVotes is one eligible title and its TMDB vote count, the popularity
// novelty is measured against.
type titleVotes struct {
	MediaID   int    `db:"media_id"`
	MediaType string `db:"media_type"`
	Votes     int    `db:"vote_count"`
}

// popularity holds each eligible title's place by vote count within its own
// type: 0 the most voted, 1 the least. films and series are the eligible
// catalog sizes, the coverage denominators.
type popularity struct {
	place         map[int]float64
	films, series int
}

func newPopularity(titles []titleVotes) popularity {
	p := popularity{place: make(map[int]float64, len(titles))}
	byType := map[string][]titleVotes{}
	for _, t := range titles {
		byType[t.MediaType] = append(byType[t.MediaType], t)
	}
	p.films, p.series = len(byType["movie"]), len(byType["tv"])
	for _, list := range byType {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Votes > list[j].Votes })
		last := float64(len(list) - 1)
		for i := 0; i < len(list); {
			// Titles with equal votes share the mean of their places.
			j := i
			for j < len(list) && list[j].Votes == list[i].Votes {
				j++
			}
			place := 0.0
			if last > 0 {
				place = (float64(i+j-1) / 2) / last
			}
			for _, t := range list[i:j] {
				p.place[t.MediaID] = place
			}
			i = j
		}
	}
	return p
}

// loadPopularity reads the vote counts of the eligible catalog, the filters
// catalogSize applies, keyed by media id as the feeds are.
func loadPopularity(ctx context.Context, dbConn *sqlx.DB) (popularity, error) {
	var titles []titleVotes
	err := dbConn.SelectContext(ctx, &titles, `
		select mi.media_id, 'movie' as media_type, m.vote_count
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where m.vote_count > 0
		union all
		select mi.media_id, 'tv', s.vote_count
		from tv_series s
		join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
		where s.vote_count > 0
	`)
	if err != nil {
		return popularity{}, fmt.Errorf("load popularity: %w", err)
	}
	return newPopularity(titles), nil
}

// exposure tracks what a feed shows across trials: the distinct titles that
// reach its top 10 (coverage) and how far each pick sits from the most
// popular titles (novelty).
type exposure struct {
	shown    map[int]bool
	placeSum float64
	placed   int
}

func (e *exposure) add(top []int, pop popularity) {
	if e.shown == nil {
		e.shown = map[int]bool{}
	}
	for _, id := range top {
		e.shown[id] = true
		if place, ok := pop.place[id]; ok {
			e.placeSum += place
			e.placed++
		}
	}
}

// coverage is the share of a catalog of the given size that reached a top 10.
func (e exposure) coverage(catalog int) float64 {
	if catalog == 0 {
		return 0
	}
	return float64(len(e.shown)) / float64(catalog)
}

// novelty is the mean popularity place of the picks shown: 0 means every pick
// was the most voted title, 1 the least.
func (e exposure) novelty() float64 {
	if e.placed == 0 {
		return 0
	}
	return e.placeSum / float64(e.placed)
}

// feedEval is one feed's numbers over a set of trials.
type feedEval struct {
	stats looStats
	exp   exposure
}

// viewOrder names the feeds the metrics print for, as the ?media= filter: the
// mixed feed over every trial, then the films and shows feeds over the trials
// that hid a film or a show.
var viewOrder = []string{"all", "movie", "tv"}

type views map[string]*feedEval

func newViews() views {
	v := views{}
	for _, name := range viewOrder {
		v[name] = &feedEval{}
	}
	return v
}

func (v views) add(tr trialResult, pop popularity) {
	v["all"].stats.add(tr.mixedRank)
	v["all"].exp.add(tr.mixedTop, pop)
	own := v["movie"]
	if tr.hiddenType == "tv" {
		own = v["tv"]
	}
	own.stats.add(tr.ownRank)
	own.exp.add(tr.ownTop, pop)
}

// catalogFor is the coverage denominator of a feed.
func (p popularity) catalogFor(view string) int {
	switch view {
	case "movie":
		return p.films
	case "tv":
		return p.series
	}
	return p.films + p.series
}

// printViews prints the per-feed table under the aggregate.
func printViews(w io.Writer, v views, pop popularity) {
	fmt.Fprintln(w, "feed   trials  recall@10 recall@50   MRR  NDCG@10  coverage  novelty")
	for _, name := range viewOrder {
		f := v[name]
		fmt.Fprintf(w, "%-6s %6d  %9.3f %9.3f %5.3f %8.3f %9.4f %8.3f\n", name, f.stats.trials,
			f.stats.recall10(), f.stats.recall50(), f.stats.mrr(), f.stats.ndcg10(),
			f.exp.coverage(pop.catalogFor(name)), f.exp.novelty())
	}
}

// variantResult is one scorer variant's run, for the report.
type variantResult struct {
	name  string
	views views
}

// writeReport writes a Markdown table with a row per variant and feed.
func writeReport(w io.Writer, results []variantResult, pop popularity) error {
	if _, err := fmt.Fprintln(w, "| variant | feed | trials | recall@10 | recall@50 | MRR | NDCG@10 | coverage | novelty |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---:|---:|---:|"); err != nil {
		return err
	}
	for _, r := range results {
		for _, name := range viewOrder {
			f := r.views[name]
			if _, err := fmt.Fprintf(w, "| %s | %s | %d | %.3f | %.3f | %.3f | %.3f | %.4f | %.3f |\n",
				r.name, name, f.stats.trials, f.stats.recall10(), f.stats.recall50(), f.stats.mrr(),
				f.stats.ndcg10(), f.exp.coverage(pop.catalogFor(name)), f.exp.novelty()); err != nil {
				return err
			}
		}
	}
	return nil
}
