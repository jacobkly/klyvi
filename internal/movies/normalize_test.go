package movies_test

import (
	"encoding/json"
	"testing"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/tmdb"
)

// The cache stores trimmed credits: a full TMDB credits object is most of an
// ~89 KB row, and the detail pages read only the top billing and a few jobs.
func TestNormalizeTMDBMovie_StoresSlimCredits(t *testing.T) {
	cast := make([]any, 0, 50)
	for i := range 50 {
		cast = append(cast, map[string]any{"id": float64(i), "name": "Actor", "order": float64(i)})
	}
	raw := map[string]any{
		"id": float64(550),
		"credits": map[string]any{
			"cast": cast,
			"crew": []any{
				map[string]any{"id": float64(7467), "name": "David Fincher", "job": "Director"},
				map[string]any{"id": float64(1), "name": "Grip", "job": "Key Grip"},
			},
		},
	}

	m := movies.NormalizeTMDBMovie(raw)

	var got struct {
		Cast []json.RawMessage `json:"cast"`
		Crew []struct {
			Job string `json:"job"`
		} `json:"crew"`
	}
	if err := json.Unmarshal(*m.Credits, &got); err != nil {
		t.Fatalf("stored credits: %v", err)
	}
	if len(got.Cast) != tmdb.CastLimit {
		t.Errorf("stored %d cast, want %d", len(got.Cast), tmdb.CastLimit)
	}
	if len(got.Crew) != 1 || got.Crew[0].Job != "Director" {
		t.Errorf("stored crew = %+v, want only the Director", got.Crew)
	}
}
