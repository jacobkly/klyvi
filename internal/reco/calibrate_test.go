package reco

import (
	"math"
	"reflect"
	"testing"
)

func genred(id int, score float64, genres ...int) Scored {
	return Scored{
		Candidate: Candidate{MediaID: id, MediaType: "movie",
			Features: &MediaFeatures{MediaID: id, MediaType: "movie", GenreIDs: genres}},
		Score: score,
	}
}

func likedWith(weight float64, genres ...int) SignalItem {
	return SignalItem{Weight: weight, Features: &MediaFeatures{GenreIDs: genres}}
}

const horror, comedy, drama = 27, 35, 18

func TestGenreTarget(t *testing.T) {
	tests := []struct {
		name  string
		liked []SignalItem
		want  map[int]float64
	}{
		{"one genre each", []SignalItem{likedWith(1, horror), likedWith(1, horror), likedWith(1, comedy), likedWith(1, drama)},
			map[int]float64{horror: 0.5, comedy: 0.25, drama: 0.25}},
		{"a title spreads its weight over its genres", []SignalItem{likedWith(1, horror, comedy)},
			map[int]float64{horror: 0.5, comedy: 0.5}},
		{"stronger likes count for more", []SignalItem{likedWith(3, horror), likedWith(1, comedy)},
			map[int]float64{horror: 0.75, comedy: 0.25}},
		{"titles with no genres or features add nothing", []SignalItem{likedWith(1, drama), likedWith(1), {Weight: 1}},
			map[int]float64{drama: 1}},
		{"no likes, no target", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GenreTarget(tc.liked)
			if len(got) != len(tc.want) {
				t.Fatalf("target = %v, want %v", got, tc.want)
			}
			for g, w := range tc.want {
				if math.Abs(got[g]-w) > 1e-9 {
					t.Errorf("target[%d] = %v, want %v", g, got[g], w)
				}
			}
		})
	}
}

func TestCalibrationError(t *testing.T) {
	target := map[int]float64{horror: 0.5, comedy: 0.5}
	matched := []Scored{genred(1, 0, horror), genred(2, 0, comedy)}
	skewed := []Scored{genred(1, 0, horror), genred(2, 0, horror)}

	if got := CalibrationError(target, matched); got > 1e-9 {
		t.Errorf("a feed matching the target has error %v, want 0", got)
	}
	// All horror: q~(comedy) is the smoothing share alone, α·0.5.
	want := 0.5*math.Log(0.5/((1-calibrationSmoothing)*1+calibrationSmoothing*0.5)) +
		0.5*math.Log(0.5/(calibrationSmoothing*0.5))
	if got := CalibrationError(target, skewed); math.Abs(got-want) > 1e-9 {
		t.Errorf("an all-horror feed has error %v, want %v", got, want)
	}
	if got := CalibrationError(nil, skewed); got != 0 {
		t.Errorf("no target has error %v, want 0", got)
	}
}

// Ten horror titles outscore ten comedies. A user whose likes are half of
// each gets half of each with calibration on, and score order with it off.
func TestMMRReorderCalibrated(t *testing.T) {
	var pool []Scored
	for i := range 10 {
		pool = append(pool, genred(i+1, 10-float64(i)*0.01, horror))
	}
	for i := range 10 {
		pool = append(pool, genred(i+101, 9-float64(i)*0.01, comedy))
	}
	target := map[int]float64{horror: 0.5, comedy: 0.5}
	all := TypeBound{TV: 1, Movie: 1}
	countComedy := func(s []Scored) int {
		n := 0
		for _, p := range s {
			if p.Features.GenreIDs[0] == comedy {
				n++
			}
		}
		return n
	}

	off := MMRReorderCalibrated(pool, 6, 1, all, Calibration{Target: target})
	if n := countComedy(off); n != 0 {
		t.Errorf("weight 0 put %d comedies in the top 6, want 0", n)
	}
	if want := MMRReorderWithin(pool, 6, 1, all); !reflect.DeepEqual(off, want) {
		t.Errorf("weight 0 differs from plain MMR")
	}

	on := MMRReorderCalibrated(pool, 6, 1, all, Calibration{Weight: 2, Target: target})
	if n := countComedy(on); n != 3 {
		t.Errorf("weight 2 put %d comedies in the top 6, want 3", n)
	}
	if CalibrationError(target, on) >= CalibrationError(target, off) {
		t.Errorf("calibration did not lower the error")
	}

	// Ranking deeper never changes the top of the feed.
	deeper := MMRReorderCalibrated(pool, 12, 1, all, Calibration{Weight: 2, Target: target})
	if !reflect.DeepEqual(deeper[:6], on) {
		t.Errorf("the top 6 changed when ranking 12 deep")
	}
}

// A pick with no genres neither helps nor hurts the mix, and the feed is
// never shortened.
func TestMMRReorderCalibrated_TitlesWithoutGenres(t *testing.T) {
	pool := []Scored{genred(1, 3, horror), genred(2, 2), genred(3, 1, comedy)}
	got := MMRReorderCalibrated(pool, 3, 1, TypeBound{TV: 1, Movie: 1},
		Calibration{Weight: 1, Target: map[int]float64{horror: 0.5, comedy: 0.5}})
	if len(got) != 3 {
		t.Fatalf("got %d picks, want 3", len(got))
	}
}

// The per-step shortcut gives the same divergence as working it out afresh
// with the candidate added, including genres outside the target.
func TestGenreMix_DivergenceWithMatchesAFreshCount(t *testing.T) {
	const scifi, western = 878, 37
	target := map[int]float64{horror: 0.5, comedy: 0.3, drama: 0.2}
	picks := []Scored{genred(1, 0, horror, drama), genred(2, 0, comedy), genred(3, 0, scifi), genred(4, 0)}
	cands := []Scored{genred(5, 0, horror), genred(6, 0, comedy, western), genred(7, 0, western), genred(8, 0)}

	for n := range len(picks) + 1 {
		m := newGenreMix(target)
		for _, p := range picks[:n] {
			m.add(p.Features)
		}
		m.prepare()
		for _, c := range cands {
			want := CalibrationError(target, append(append([]Scored(nil), picks[:n]...), c))
			if got := m.divergenceWith(c.Features); math.Abs(got-want) > 1e-9 {
				t.Errorf("after %d picks, with %d: divergence %v, want %v", n, c.MediaID, got, want)
			}
		}
	}
}

func TestFeedCalibration(t *testing.T) {
	liked := []SignalItem{likedWith(1, horror), likedWith(1, comedy)}
	if got := FeedCalibration(Config{}, liked); got.on() {
		t.Errorf("weight 0 turned calibration on: %+v", got)
	}
	got := FeedCalibration(Config{CalibrationWeight: 0.5}, liked)
	if !got.on() || got.Weight != 0.5 || math.Abs(got.Target[horror]-0.5) > 1e-9 {
		t.Errorf("FeedCalibration = %+v, want weight 0.5 and a half-horror target", got)
	}
	if FeedCalibration(Config{CalibrationWeight: 0.5}, nil).on() {
		t.Errorf("no likes turned calibration on")
	}
}
