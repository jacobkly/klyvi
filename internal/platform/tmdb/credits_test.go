package tmdb

import (
	"encoding/json"
	"reflect"
	"testing"
)

func castMember(name string, order float64) map[string]any {
	return map[string]any{"id": order + 1, "name": name, "character": "", "order": order}
}

func crewMember(name, job string) map[string]any {
	return map[string]any{"id": 1.0, "name": name, "job": job, "department": "x"}
}

func names(t *testing.T, v any, key string) []string {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want map[string]any", v)
	}
	list, _ := m[key].([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.(map[string]any)["name"].(string))
	}
	return out
}

func TestSlimCredits_KeepsTheTopBilledCastInBillingOrder(t *testing.T) {
	// TMDB sends 60 cast members, the last-billed first.
	cast := make([]any, 0, 60)
	for i := 59; i >= 0; i-- {
		cast = append(cast, castMember(string(rune('A'+i%26))+string(rune('0'+i/26)), float64(i)))
	}

	got := SlimCredits(map[string]any{"cast": cast, "crew": []any{}})

	kept := got.(map[string]any)["cast"].([]any)
	if len(kept) != CastLimit {
		t.Fatalf("kept %d cast, want %d", len(kept), CastLimit)
	}
	for i, e := range kept {
		if o := e.(map[string]any)["order"].(float64); o != float64(i) {
			t.Fatalf("cast[%d].order = %v, want %d", i, o, i)
		}
	}
}

func TestSlimCredits_CastWithoutABillingOrderSortsLast(t *testing.T) {
	unbilled := map[string]any{"id": 9.0, "name": "Unbilled"}
	in := map[string]any{"cast": []any{unbilled, castMember("Lead", 0), castMember("Second", 1)}}

	got := names(t, SlimCredits(in), "cast")

	if want := []string{"Lead", "Second", "Unbilled"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cast = %v, want %v", got, want)
	}
}

func TestSlimCredits_KeepsOnlyTheCrewJobsTheDetailPageShows(t *testing.T) {
	in := map[string]any{"crew": []any{
		crewMember("Casting Person", "Casting"),
		crewMember("David Fincher", "Director"),
		crewMember("Jim Uhls", "Screenplay"),
		crewMember("Boom Op", "Boom Operator"),
		crewMember("Chuck Palahniuk", "Novel"),
		crewMember("Art Linson", "Producer"),
		crewMember("Jeff Cronenweth", "Director of Photography"),
		crewMember("Exec", "Executive Producer"),
		crewMember("James Haygood", "Editor"),
		crewMember("Dust Brothers", "Original Music Composer"),
		crewMember("Alex McDowell", "Production Design"),
		crewMember("Gaffer Person", "Gaffer"),
	}}

	got := names(t, SlimCredits(in), "crew")

	want := []string{
		"David Fincher", "Jim Uhls", "Chuck Palahniuk", "Art Linson",
		"Jeff Cronenweth", "James Haygood", "Dust Brothers", "Alex McDowell",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("crew = %v, want %v (TMDB order, shown jobs only)", got, want)
	}
}

func TestSlimCredits_KeepsOnlyTheFieldsKlyviReads(t *testing.T) {
	lead := map[string]any{
		"adult": false, "gender": 2.0, "id": 819.0, "known_for_department": "Acting",
		"name": "Edward Norton", "original_name": "Edward Norton", "popularity": 26.9,
		"profile_path": "/p.jpg", "cast_id": 4.0, "character": "Narrator",
		"credit_id": "52fe4250c3a36847f80149f3", "order": 0.0,
	}
	director := map[string]any{
		"adult": false, "gender": 2.0, "id": 7467.0, "known_for_department": "Directing",
		"name": "David Fincher", "original_name": "David Fincher", "popularity": 20.3,
		"profile_path": nil, "credit_id": "631f0289568463007bbe28a4",
		"department": "Directing", "job": "Director",
	}

	got := SlimCredits(map[string]any{"cast": []any{lead}, "crew": []any{director}}).(map[string]any)

	wantCast := map[string]any{"id": 819.0, "name": "Edward Norton", "character": "Narrator", "order": 0.0, "profile_path": "/p.jpg"}
	if c := got["cast"].([]any)[0]; !reflect.DeepEqual(c, wantCast) {
		t.Errorf("cast[0] = %v, want %v", c, wantCast)
	}
	wantCrew := map[string]any{"id": 7467.0, "name": "David Fincher", "job": "Director", "profile_path": nil}
	if c := got["crew"].([]any)[0]; !reflect.DeepEqual(c, wantCrew) {
		t.Errorf("crew[0] = %v, want %v", c, wantCrew)
	}
}

func TestSlimCreditsJSON_ReportsWhetherAnythingWasDropped(t *testing.T) {
	tests := []struct {
		name        string
		stored      string
		wantChanged bool
		wantCrew    int
	}{
		{
			name:        "full credits",
			stored:      `{"cast": [], "crew": [{"id": 1, "job": "Director", "name": "A"}, {"id": 2, "job": "Gaffer", "name": "B"}]}`,
			wantChanged: true,
			wantCrew:    1,
		},
		{
			name:        "every entry kept, but with fields Klyvi does not read",
			stored:      `{"cast": [], "crew": [{"id": 1, "job": "Director", "name": "A", "credit_id": "x", "department": "Directing"}]}`,
			wantChanged: true,
			wantCrew:    1,
		},
		{
			// Postgres prints jsonb with spaces, so byte length alone would
			// call every stored row changed on every pass.
			name:        "already slim, as Postgres prints it",
			stored:      `{"cast": [{"id": 1, "name": "A", "order": 0, "character": "B", "profile_path": null}], "crew": [{"id": 1, "job": "Director", "name": "A", "profile_path": null}]}`,
			wantChanged: false,
			wantCrew:    1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, err := SlimCreditsJSON([]byte(tt.stored))
			if err != nil {
				t.Fatalf("SlimCreditsJSON: %v", err)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			var got struct {
				Crew []any `json:"crew"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("output is not JSON: %v", err)
			}
			if len(got.Crew) != tt.wantCrew {
				t.Errorf("crew = %d entries, want %d", len(got.Crew), tt.wantCrew)
			}
		})
	}
}

func TestSlimCreditsJSON_RejectsInvalidJSON(t *testing.T) {
	if _, _, err := SlimCreditsJSON([]byte(`{"cast": [`)); err == nil {
		t.Error("want an error for truncated JSON")
	}
}

func TestSlimCredits_PassesAnythingElseThrough(t *testing.T) {
	tests := []struct {
		name string
		in   any
	}{
		{"nil", nil},
		{"not an object", []any{"x"}},
		{"object without cast or crew", map[string]any{"id": 550.0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SlimCredits(tt.in); !reflect.DeepEqual(got, tt.in) {
				t.Errorf("SlimCredits(%v) = %v, want it unchanged", tt.in, got)
			}
		})
	}
}
