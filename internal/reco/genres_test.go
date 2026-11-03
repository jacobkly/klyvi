package reco

import (
	"reflect"
	"testing"
)

func TestCanonicalTVGenres(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		want []int
	}{
		{"sci-fi and fantasy splits into both film genres", []int{10765}, []int{878, 14}},
		{"action and adventure splits into both film genres", []int{10759}, []int{28, 12}},
		{"war and politics maps to war", []int{10768}, []int{10752}},
		{"kids keeps its own id", []int{10762}, []int{10762}},
		{"news, reality, soap and talk keep their own ids", []int{10763, 10764, 10766, 10767}, []int{10763, 10764, 10766, 10767}},
		{"genres films share pass through", []int{18, 80, 35}, []int{18, 80, 35}},
		{"order is kept and duplicates dropped", []int{10759, 28, 18}, []int{28, 12, 18}},
		{"no genres", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalTVGenres(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CanonicalTVGenres(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// A taste is held in film genres, but the series cache stores TV's own ids.
// Looking a taste up among series asks for the combined TV genre as well.
func TestTVGenresFor(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		want []int
	}{
		{"science fiction also asks for sci-fi and fantasy", []int{878}, []int{878, 10765}},
		{"action and adventure share one tv genre", []int{28, 12}, []int{28, 10759, 12}},
		{"war also asks for war and politics", []int{10752}, []int{10752, 10768}},
		{"genres films share pass through", []int{18, 80}, []int{18, 80}},
		{"no genres", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TVGenresFor(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TVGenresFor(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// The series feature stats are counted over TV's own genre ids. Read into
// the film space, a combined genre's count goes to each genre it splits into.
func TestCanonicalTVGenreDF(t *testing.T) {
	got := canonicalTVGenreDF(map[int]int{10765: 1493, 10768: 339, 18: 2587, 10762: 727})
	want := map[int]int{878: 1493, 14: 1493, 10752: 339, 18: 2587, 10762: 727}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("canonicalTVGenreDF = %v, want %v", got, want)
	}
}
