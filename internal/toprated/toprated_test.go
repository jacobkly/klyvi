package toprated

import (
	"math"
	"testing"
)

func TestMoviePath(t *testing.T) {
	got := MoviePath(3)
	want := "/discover/movie?include_adult=false&language=en-US&page=3&sort_by=vote_average.desc&vote_count.gte=2000"
	if got != want {
		t.Errorf("MoviePath(3) = %q, want %q", got, want)
	}
}

func TestTVPath(t *testing.T) {
	got := TVPath(1)
	want := "/discover/tv?include_adult=false&language=en-US&page=1&sort_by=vote_average.desc&vote_count.gte=500&without_genres=10767,10763,10764,10766"
	if got != want {
		t.Errorf("TVPath(1) = %q, want %q", got, want)
	}
}

func TestWeighted(t *testing.T) {
	// At the floor a title sits halfway between its own score and the mean.
	if got := Weighted(9.0, 2000, 2000, 7.0); math.Abs(got-8.0) > 1e-9 {
		t.Errorf("Weighted at the floor = %v, want 8.0", got)
	}
	// With far more votes than the floor it keeps nearly all of its own.
	big := Weighted(8.7, 20000, 2000, 7.5)
	small := Weighted(9.0, 2100, 2000, 7.5)
	if big <= small {
		t.Errorf("8.7 with 20000 votes (%v) should outrank 9.0 with 2100 (%v)", big, small)
	}
	if got := Weighted(8, 0, 0, 7); got != 7 {
		t.Errorf("Weighted with no votes and no floor = %v, want the mean", got)
	}
}

func TestExcludedTV(t *testing.T) {
	if !ExcludedTV([]int{18, 10767}) {
		t.Error("a talk show should be left out")
	}
	if ExcludedTV([]int{18, 80}) {
		t.Error("a crime drama should stay in")
	}
}
