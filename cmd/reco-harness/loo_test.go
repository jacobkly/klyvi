package main

import (
	"math"
	"reflect"
	"testing"

	"klyvi-api/internal/reco"
)

func rating(n int) *int { return &n }

// A trial hides a film or a series. Seasons count towards their series, as the
// signal rolls them up, and a season with no indexed series is skipped.
func TestLikedUnits_RollsSeasonsUpToTheirSeries(t *testing.T) {
	rows := []reco.InteractionRow{
		{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: rating(90)},
		{MediaID: 501, MediaType: "season", Kind: "rated", Rating: rating(80), SeriesMediaID: 50},
		{MediaID: 502, MediaType: "season", Kind: "logged", SeriesMediaID: 50},
		{MediaID: 60, MediaType: "tv", Kind: "rated", Rating: rating(40)},
		{MediaID: 2, MediaType: "movie", Kind: "impression"},
		{MediaID: 503, MediaType: "season", Kind: "rated", Rating: rating(90)},
		{MediaID: 70, MediaType: "tv", Kind: "logged"},
	}
	want := []likedUnit{{1, "movie"}, {50, "tv"}, {70, "tv"}}
	if got := likedUnits(rows); !reflect.DeepEqual(got, want) {
		t.Errorf("likedUnits = %v, want %v", got, want)
	}
}

// A film logged and rated is one thing the user liked, so it is one trial.
func TestLikedUnits_CountsAFilmOnce(t *testing.T) {
	rows := []reco.InteractionRow{
		{MediaID: 1, MediaType: "movie", Kind: "logged"},
		{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: rating(90)},
	}
	if got := likedUnits(rows); len(got) != 1 {
		t.Errorf("likedUnits = %v, want one film", got)
	}
}

// Hiding a unit hides every row of it. A row left behind would keep the
// hidden item in the seen set, and the trial could never recover it.
func TestWithoutUnit_HidesEveryRowOfTheUnit(t *testing.T) {
	rows := []reco.InteractionRow{
		{MediaID: 1, MediaType: "movie", Kind: "logged"},
		{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: rating(90)},
		{MediaID: 50, MediaType: "tv", Kind: "rated", Rating: rating(90)},
		{MediaID: 501, MediaType: "season", Kind: "logged", SeriesMediaID: 50},
		{MediaID: 2, MediaType: "movie", Kind: "logged"},
	}
	cases := []struct {
		hide likedUnit
		want []int
	}{
		{likedUnit{1, "movie"}, []int{50, 501, 2}},
		{likedUnit{50, "tv"}, []int{1, 1, 2}},
	}
	for _, tc := range cases {
		var got []int
		for _, it := range withoutUnit(rows, tc.hide) {
			got = append(got, it.MediaID)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("without %v left %v, want %v", tc.hide, got, tc.want)
		}
	}
}

// A tracked season keeps its series out of the feed, as the API's seen set
// does.
func TestSeenIn_AddsTheSeriesOfASeason(t *testing.T) {
	seen := seenIn([]reco.InteractionRow{
		{MediaID: 1, MediaType: "movie"},
		{MediaID: 501, MediaType: "season", SeriesMediaID: 50},
	})
	for _, id := range []int{1, 501, 50} {
		if !seen[id] {
			t.Errorf("media %d is not in the seen set %v", id, seen)
		}
	}
}

func TestHistoryGroup(t *testing.T) {
	cases := []struct {
		units []likedUnit
		want  string
	}{
		{[]likedUnit{{1, "movie"}, {2, "movie"}}, "films"},
		{[]likedUnit{{50, "tv"}}, "shows"},
		{[]likedUnit{{1, "movie"}, {50, "tv"}}, "mixed"},
	}
	for _, tc := range cases {
		if got := historyGroup(tc.units); got != tc.want {
			t.Errorf("historyGroup(%v) = %q, want %q", tc.units, got, tc.want)
		}
	}
}

// Rank 0 is a miss. A hit anywhere in the feed adds its reciprocal rank.
func TestLOOStats(t *testing.T) {
	var s looStats
	for _, rank := range []int{1, 12, 40, 0} {
		s.add(rank)
	}
	if s.trials != 4 || s.recall10() != 0.25 || s.recall50() != 0.75 {
		t.Errorf("trials=%d recall@10=%.3f recall@50=%.3f, want 4, 0.25 and 0.75",
			s.trials, s.recall10(), s.recall50())
	}
	if want := (1 + 1.0/12 + 1.0/40) / 4; math.Abs(s.mrr()-want) > 1e-9 {
		t.Errorf("MRR = %.4f, want %.4f", s.mrr(), want)
	}

	var total looStats
	total.merge(s)
	total.merge(s)
	if total.trials != 8 || total.recall50() != 0.75 {
		t.Errorf("merged trials=%d recall@50=%.3f, want 8 and 0.75", total.trials, total.recall50())
	}
	if (looStats{}).mrr() != 0 {
		t.Error("an empty row has a nonzero MRR")
	}
}

// A trial counts once in each feed, a miss in one and a hit in the other.
func TestFeedStats_CountsEachFeed(t *testing.T) {
	var s feedStats
	s.add(trialResult{hiddenType: "tv", mixedRank: 0, ownRank: 3})
	s.add(trialResult{hiddenType: "tv", mixedRank: 30, ownRank: 8})
	if s.mixed.trials != 2 || s.mixed.hits10 != 0 || s.mixed.hits50 != 1 {
		t.Errorf("mixed = %+v, want 2 trials, no hit at 10 and one at 50", s.mixed)
	}
	if s.own.trials != 2 || s.own.hits10 != 2 {
		t.Errorf("own-type = %+v, want 2 trials, both hits at 10", s.own)
	}
}

func TestBreakdownKey(t *testing.T) {
	cases := []struct{ group, hidden, want string }{
		{"films", "movie", "films/film"},
		{"shows", "tv", "shows/show"},
		{"mixed", "movie", "mixed/film"},
		{"mixed", "tv", "mixed/show"},
	}
	for _, tc := range cases {
		if got := breakdownKey(tc.group, tc.hidden); got != tc.want {
			t.Errorf("breakdownKey(%q, %q) = %q, want %q", tc.group, tc.hidden, got, tc.want)
		}
	}
}

// The cross-media check counts a films-only taste's shows and where the
// related ones rank.
func TestCrossMediaHits(t *testing.T) {
	ranked := []reco.Scored{
		{Candidate: reco.Candidate{MediaID: 1, MediaType: "movie"}},
		{Candidate: reco.Candidate{MediaID: 50, MediaType: "tv"}},
		{Candidate: reco.Candidate{MediaID: 51, MediaType: "tv"}},
		{Candidate: reco.Candidate{MediaID: 52, MediaType: "tv"}},
	}
	shows, related := crossMediaHits(ranked, map[int]bool{52: true, 50: true, 99: true})
	if shows != 3 {
		t.Errorf("shows = %d, want 3", shows)
	}
	if want := []int{2, 4}; !reflect.DeepEqual(related, want) {
		t.Errorf("related ranks = %v, want %v", related, want)
	}
}
