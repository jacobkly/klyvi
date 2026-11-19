package movies

import (
	"testing"

	"klyvi-api/internal/toprated"
)

// The list endpoint is what the frontend's browse pagination rides on:
// page must pass through to TMDB, clamped to TMDB's real bounds (1..500).
func TestListEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		listType string
		page     int
		want     string
		wantErr  bool
	}{
		{"popular page 1", "popular", 1, "/movie/popular?language=en-US&page=1", false},
		{"popular page 7", "popular", 7, "/movie/popular?language=en-US&page=7", false},
		{"upcoming page 2", "upcoming", 2, "/movie/upcoming?language=en-US&page=2", false},
		{"top_rated page 3", "top_rated", 3, toprated.MoviePath(3), false},
		{"trending page 2", "trending", 2, "/trending/movie/week?language=en-US&page=2", false},

		// Out-of-range pages clamp rather than 400: the client dedupes by id
		// and a clamped page is more useful than an error mid-scroll.
		{"page zero clamps to 1", "popular", 0, "/movie/popular?language=en-US&page=1", false},
		{"negative page clamps to 1", "popular", -3, "/movie/popular?language=en-US&page=1", false},
		{"page above TMDB max clamps to 500", "popular", 501, "/movie/popular?language=en-US&page=500", false},

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
