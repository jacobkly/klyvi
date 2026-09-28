package tv

import (
	"encoding/json"
	"testing"

	"klyvi-api/internal/platform/tmdb"
)

// Series credits are trimmed the same way as movies before they are cached.
func TestNormalizeTMDBTVSeries_StoresSlimCredits(t *testing.T) {
	cast := make([]any, 0, 50)
	for i := range 50 {
		cast = append(cast, map[string]any{"id": float64(i), "name": "Actor", "order": float64(i)})
	}
	raw := map[string]any{
		"id": float64(1396),
		"credits": map[string]any{
			"cast": cast,
			"crew": []any{
				map[string]any{"id": float64(66633), "name": "Vince Gilligan", "job": "Producer"},
				map[string]any{"id": float64(1), "name": "Mixer", "job": "Sound Re-Recording Mixer"},
			},
		},
	}

	s := NormalizeTMDBTVSeries(raw)

	var got struct {
		Cast []json.RawMessage `json:"cast"`
		Crew []struct {
			Job string `json:"job"`
		} `json:"crew"`
	}
	if err := json.Unmarshal(*s.Credits, &got); err != nil {
		t.Fatalf("stored credits: %v", err)
	}
	if len(got.Cast) != tmdb.CastLimit {
		t.Errorf("stored %d cast, want %d", len(got.Cast), tmdb.CastLimit)
	}
	if len(got.Crew) != 1 || got.Crew[0].Job != "Producer" {
		t.Errorf("stored crew = %+v, want only the Producer", got.Crew)
	}
}
