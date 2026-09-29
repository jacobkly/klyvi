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

// A season's episodes are cached without their per-episode crew and guest
// stars, which nothing reads and which make up most of the payload.
func TestNormalizeTMDBTVSeason_DropsEpisodeCredits(t *testing.T) {
	raw := map[string]any{
		"season_number": float64(1),
		"episodes": []any{
			map[string]any{
				"id":             float64(62085),
				"episode_number": float64(1),
				"name":           "Pilot",
				"runtime":        float64(58),
				"still_path":     "/pilot.jpg",
				"crew":           []any{map[string]any{"id": float64(66633), "job": "Director"}},
				"guest_stars":    []any{map[string]any{"id": float64(1), "name": "Guest"}},
			},
		},
	}

	s := NormalizeTMDBTVSeason(raw, 1396)

	var got []map[string]json.RawMessage
	if err := json.Unmarshal(*s.Episodes, &got); err != nil {
		t.Fatalf("stored episodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("stored %d episodes, want 1", len(got))
	}
	for _, dropped := range []string{"crew", "guest_stars"} {
		if _, ok := got[0][dropped]; ok {
			t.Errorf("episode still carries %q", dropped)
		}
	}
	for _, kept := range []string{"id", "episode_number", "name", "runtime", "still_path"} {
		if _, ok := got[0][kept]; !ok {
			t.Errorf("episode lost %q", kept)
		}
	}
}
