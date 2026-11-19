package tv

import (
	"testing"

	"klyvi-api/internal/toprated"
)

// Mirrors the movies list test: page passes through to TMDB, clamped 1..500.
func TestListEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		listType string
		page     int
		want     string
		wantErr  bool
	}{
		{"popular page 1", "popular", 1, "/tv/popular?language=en-US&page=1", false},
		{"popular page 4", "popular", 4, "/tv/popular?language=en-US&page=4", false},
		{"upcoming page 2", "upcoming", 2, "/tv/on_the_air?language=en-US&page=2", false},
		{"top_rated page 5", "top_rated", 5, toprated.TVPath(5), false},
		{"trending page 3", "trending", 3, "/trending/tv/week?language=en-US&page=3", false},

		{"page zero clamps to 1", "popular", 0, "/tv/popular?language=en-US&page=1", false},
		{"page above TMDB max clamps to 500", "popular", 999, "/tv/popular?language=en-US&page=500", false},

		{"invalid list type errors", "bogus", 1, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := listEndpoint(tt.listType, tt.page)
			if (err != nil) != tt.wantErr {
				t.Fatalf("listEndpoint(%q, %d) error = %v, wantErr %v", tt.listType, tt.page, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("listEndpoint(%q, %d) = %q, want %q", tt.listType, tt.page, got, tt.want)
			}
		})
	}
}
