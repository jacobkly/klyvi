package rankings

import "testing"

// A trimmed-down TMDB top_rated payload: two pages worth, one duplicate
// across pages, to prove dedup and rank assignment.
func sampleResults() []map[string]any {
	return []map[string]any{
		{"id": float64(238), "title": "The Godfather", "poster_path": "/gf.jpg", "release_date": "1972-03-14", "vote_average": 8.7, "genre_ids": []any{float64(18), float64(80)}},
		{"id": float64(278), "title": "The Shawshank Redemption", "poster_path": "/sr.jpg", "release_date": "1994-09-23", "vote_average": 8.7, "genre_ids": []any{float64(18)}},
		{"id": float64(238), "title": "The Godfather", "poster_path": "/gf.jpg", "release_date": "1972-03-14", "vote_average": 8.7, "genre_ids": []any{float64(18)}}, // dup
	}
}

func TestNormalizeRankings_MoviesDedupAndRank(t *testing.T) {
	got := normalizeRankings(sampleResults(), mediaMovie, nil)

	if len(got) != 2 {
		t.Fatalf("rankings = %d, want 2 (dup removed)", len(got))
	}
	if got[0].Rank != 1 || got[1].Rank != 2 {
		t.Errorf("ranks = %d,%d, want 1,2", got[0].Rank, got[1].Rank)
	}
	if got[0].TMDBID != 238 || got[0].Title != "The Godfather" {
		t.Errorf("first = %+v, want The Godfather (238)", got[0])
	}
	if got[0].Year != 1972 {
		t.Errorf("year = %d, want 1972", got[0].Year)
	}
	// Genre ids map to names.
	if len(got[0].Genres) != 2 || got[0].Genres[0] != "Drama" || got[0].Genres[1] != "Crime" {
		t.Errorf("genres = %v, want [Drama Crime]", got[0].Genres)
	}
}

func TestNormalizeRankings_TVUsesNameAndFirstAirDate(t *testing.T) {
	results := []map[string]any{
		{"id": float64(1396), "name": "Breaking Bad", "poster_path": "/bb.jpg", "first_air_date": "2008-01-20", "vote_average": 8.9, "genre_ids": []any{float64(18)}},
	}
	got := normalizeRankings(results, mediaTV, nil)

	if len(got) != 1 {
		t.Fatalf("rankings = %d, want 1", len(got))
	}
	if got[0].Title != "Breaking Bad" {
		t.Errorf("title = %q, want Breaking Bad", got[0].Title)
	}
	if got[0].Year != 2008 {
		t.Errorf("year = %d, want 2008", got[0].Year)
	}
}

func TestNormalizeRankings_CapsAt100(t *testing.T) {
	var results []map[string]any
	for i := 0; i < 150; i++ {
		results = append(results, map[string]any{
			"id":    float64(i + 1),
			"title": "Film",
		})
	}
	got := normalizeRankings(results, mediaMovie, nil)
	if len(got) != 100 {
		t.Errorf("rankings = %d, want capped at 100", len(got))
	}
	if got[99].Rank != 100 {
		t.Errorf("last rank = %d, want 100", got[99].Rank)
	}
}

func TestNormalizeRankings_FillsMediaID(t *testing.T) {
	resolved := map[int]int{238: 4242}
	got := normalizeRankings(sampleResults(), mediaMovie, resolved)

	if got[0].MediaID == nil || *got[0].MediaID != 4242 {
		t.Errorf("media_id = %v, want 4242", got[0].MediaID)
	}
	// Shawshank is not in the resolved map — media_id stays null.
	if got[1].MediaID != nil {
		t.Errorf("unresolved media_id = %v, want nil", got[1].MediaID)
	}
}
