package reco_test

import (
	"encoding/json"
	"strings"
	"testing"

	"klyvi-api/internal/reco"
)

// The feed is the one payload that must stay in step with the rest of the API's
// snake_case contract — Candidate/Scored embed and flatten, so a missing tag
// silently leaks a PascalCase key to the frontend.
func TestScored_MarshalsSnakeCase(t *testing.T) {
	s := reco.Scored{
		Candidate: reco.Candidate{
			MediaID: 1, MediaType: "movie", TMDBID: 550, Title: "Fight Club",
			PosterPath: "/p.jpg", BackdropPath: "/b.jpg", ReleaseYear: 1999, VoteAverage: 8.4,
			Features: &reco.MediaFeatures{MediaID: 1, GenreIDs: []int{18}, KeywordIDs: []int{825}},
		},
		Score:   0.9,
		Reasons: []reco.Reason{{Kind: "genre", ID: 18, Name: "Drama"}},
	}

	got := string(mustMarshal(t, s))

	for _, key := range []string{
		`"media_id"`, `"media_type"`, `"tmdb_id"`, `"poster_path"`, `"backdrop_path"`,
		`"release_year"`, `"vote_average"`, `"score"`, `"reasons"`,
		`"genre_ids"`, `"keyword_ids"`,
	} {
		if !strings.Contains(got, key) {
			t.Errorf("missing snake_case key %s in %s", key, got)
		}
	}
	for _, bad := range []string{`"MediaID"`, `"Title"`, `"Score"`, `"GenreIDs"`} {
		if strings.Contains(got, bad) {
			t.Errorf("found PascalCase key %s in %s", bad, got)
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
