package main

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// With one relevant item, NDCG@10 is 1/log2(rank+1) inside the top 10 and 0
// outside it, so a first-place hit scores 1 and a third-place hit 0.5.
func TestNDCGAt10(t *testing.T) {
	cases := []struct {
		rank int
		want float64
	}{
		{1, 1},
		{3, 0.5},
		{10, 1 / math.Log2(11)},
		{11, 0},
		{0, 0},
	}
	for _, c := range cases {
		if got := ndcgAt(c.rank, 10); !near(got, c.want) {
			t.Errorf("ndcgAt(%d, 10) = %v, want %v", c.rank, got, c.want)
		}
	}
}

// looStats averages NDCG@10 over every trial, misses included.
func TestLOOStats_NDCG(t *testing.T) {
	var s looStats
	for _, r := range []int{1, 3, 0, 60} {
		s.add(r)
	}
	if want := 1.5 / 4; !near(s.ndcg10(), want) {
		t.Errorf("ndcg10 = %v, want %v", s.ndcg10(), want)
	}
	var m looStats
	m.merge(s)
	if !near(m.ndcg10(), s.ndcg10()) {
		t.Errorf("merged ndcg10 = %v, want %v", m.ndcg10(), s.ndcg10())
	}
}

// Popularity places each title within its own type by vote count, 0 the most
// voted and 1 the least. Ties share the mean of their places.
func TestNewPopularity(t *testing.T) {
	pop := newPopularity([]titleVotes{
		{MediaID: 1, MediaType: "movie", Votes: 900},
		{MediaID: 2, MediaType: "movie", Votes: 50},
		{MediaID: 3, MediaType: "movie", Votes: 50},
		{MediaID: 4, MediaType: "movie", Votes: 5},
		{MediaID: 10, MediaType: "tv", Votes: 7},
	})
	want := map[int]float64{1: 0, 2: 0.5, 3: 0.5, 4: 1, 10: 0}
	for id, w := range want {
		if got, ok := pop.place[id]; !ok || !near(got, w) {
			t.Errorf("place[%d] = %v (%v), want %v", id, got, ok, w)
		}
	}
	if pop.films != 4 || pop.series != 1 {
		t.Errorf("sizes = %d films, %d series, want 4 and 1", pop.films, pop.series)
	}
}

// Coverage is the share of the catalog that ever reaches a top 10, counting
// each title once. Novelty is the mean popularity place of every pick shown.
func TestExposure(t *testing.T) {
	pop := newPopularity([]titleVotes{
		{MediaID: 1, MediaType: "movie", Votes: 900},
		{MediaID: 2, MediaType: "movie", Votes: 500},
		{MediaID: 3, MediaType: "movie", Votes: 100},
		{MediaID: 4, MediaType: "movie", Votes: 10},
		{MediaID: 5, MediaType: "movie", Votes: 1},
	})
	var e exposure
	e.add([]int{1, 2}, pop)
	e.add([]int{2, 5}, pop)
	if got := e.coverage(pop.films); !near(got, 3.0/5) {
		t.Errorf("coverage = %v, want 0.6", got)
	}
	// Places: 1 is 0, 2 is 0.25, 5 is 1. Picks shown: 0, 0.25, 0.25, 1.
	if got := e.novelty(); !near(got, 1.5/4) {
		t.Errorf("novelty = %v, want 0.375", got)
	}
	var empty exposure
	if empty.coverage(10) != 0 || empty.novelty() != 0 {
		t.Error("an empty exposure should read 0")
	}
}

// A pick missing from the popularity table (no votes) still counts towards
// coverage but adds nothing to novelty, rather than skewing it.
func TestExposure_SkipsUnknownTitlesForNovelty(t *testing.T) {
	pop := newPopularity([]titleVotes{
		{MediaID: 1, MediaType: "movie", Votes: 9},
		{MediaID: 2, MediaType: "movie", Votes: 1},
	})
	var e exposure
	e.add([]int{2, 99}, pop)
	if got := e.novelty(); !near(got, 1) {
		t.Errorf("novelty = %v, want 1", got)
	}
	if got := e.coverage(2); !near(got, 1) {
		t.Errorf("coverage = %v, want 1", got)
	}
}

// Each trial feeds the mixed view, and the view of its own type: a hidden
// film counts in the films feed, a hidden show in the shows feed.
func TestViews_RouteTrialsByType(t *testing.T) {
	pop := newPopularity([]titleVotes{{MediaID: 1, MediaType: "movie", Votes: 3}})
	v := newViews()
	v.add(trialResult{hiddenType: "movie", mixedRank: 1, ownRank: 2, mixedTop: []int{1}, ownTop: []int{1}}, pop)
	v.add(trialResult{hiddenType: "tv", mixedRank: 0, ownRank: 5}, pop)
	if v["all"].stats.trials != 2 || v["movie"].stats.trials != 1 || v["tv"].stats.trials != 1 {
		t.Fatalf("trials all=%d movie=%d tv=%d, want 2, 1, 1",
			v["all"].stats.trials, v["movie"].stats.trials, v["tv"].stats.trials)
	}
	if v["movie"].stats.hits10 != 1 || v["tv"].stats.hits10 != 1 || v["all"].stats.hits10 != 1 {
		t.Error("the own-type views should count the own-type rank, the mixed view the mixed rank")
	}
}

// The report is one Markdown table: a row per variant and feed, in order.
func TestWriteReport(t *testing.T) {
	pop := newPopularity([]titleVotes{
		{MediaID: 1, MediaType: "movie", Votes: 3},
		{MediaID: 2, MediaType: "tv", Votes: 3},
	})
	base := newViews()
	base.add(trialResult{hiddenType: "movie", mixedRank: 1, ownRank: 1, mixedTop: []int{1}, ownTop: []int{1}}, pop)
	other := newViews()
	other.add(trialResult{hiddenType: "movie", mixedRank: 0, ownRank: 0}, pop)

	var buf bytes.Buffer
	if err := writeReport(&buf, []variantResult{{"current", base}, {"no IDF", other}}, pop); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var rows []string
	for _, l := range lines {
		if strings.HasPrefix(l, "| ") && !strings.HasPrefix(l, "| variant") {
			rows = append(rows, l)
		}
	}
	if len(rows) != 6 {
		t.Fatalf("got %d rows, want 6 (2 variants by 3 feeds):\n%s", len(rows), buf.String())
	}
	if !strings.HasPrefix(rows[0], "| current | all | 1 | 1.000 | 1.000 | 1.000 | 1.000 |") {
		t.Errorf("first row = %q", rows[0])
	}
	if !strings.HasPrefix(rows[3], "| no IDF | all | 1 | 0.000 |") {
		t.Errorf("fourth row = %q", rows[3])
	}
	if !strings.Contains(buf.String(), "|---") {
		t.Error("missing the Markdown header rule")
	}
}
