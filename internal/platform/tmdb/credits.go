package tmdb

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
)

// CastLimit is how many billed cast members a cached credits object keeps.
// The detail pages show 30.
const CastLimit = 40

// shownCrewJobs are the crew jobs the web detail pages group into roles
// (CREW_ROLES in klyvi-web/lib/api/map.ts). Every other job is dropped.
var shownCrewJobs = map[string]bool{
	"Director":                true,
	"Screenplay":              true,
	"Writer":                  true,
	"Story":                   true,
	"Author":                  true,
	"Novel":                   true,
	"Producer":                true,
	"Director of Photography": true,
	"Editor":                  true,
	"Original Music Composer": true,
	"Music":                   true,
	"Production Design":       true,
}

// The fields a kept entry carries. TMDB sends a dozen per person (gender,
// popularity, credit_id, ...) that nothing in Klyvi reads.
var (
	castFields = []string{"id", "name", "character", "order", "profile_path"}
	crewFields = []string{"id", "name", "job", "profile_path"}
)

// SlimCredits trims a decoded TMDB credits object ({"cast": [...], "crew":
// [...]}) to what Klyvi reads: the first CastLimit cast members by billing
// order, and the crew whose job the detail pages show, in TMDB's order, each
// cut down to castFields or crewFields. Full credits run to ~80 KB for a big
// film, which makes every detail read cost several round trips. Anything that
// is not a credits object passes through untouched.
func SlimCredits(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	_, hasCast := m["cast"]
	_, hasCrew := m["crew"]
	if !hasCast && !hasCrew {
		return v
	}

	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = val
	}
	if cast, ok := m["cast"].([]any); ok {
		out["cast"] = pick(topBilled(cast), castFields)
	}
	if crew, ok := m["crew"].([]any); ok {
		out["crew"] = pick(shownCrew(crew), crewFields)
	}
	return out
}

// SlimCreditsJSON is SlimCredits for a stored credits column. changed reports
// whether trimming altered anything, so a backfill rewrites only the rows
// cached before the trim. It compares decoded values, because Postgres prints
// jsonb with spaces that encoding/json does not.
func SlimCreditsJSON(stored []byte) (out []byte, changed bool, err error) {
	var v any
	if err := json.Unmarshal(stored, &v); err != nil {
		return nil, false, fmt.Errorf("decode credits: %w", err)
	}
	slim := SlimCredits(v)
	out, err = json.Marshal(slim)
	if err != nil {
		return nil, false, fmt.Errorf("encode credits: %w", err)
	}
	return out, !reflect.DeepEqual(slim, v), nil
}

func topBilled(cast []any) []any {
	sorted := make([]any, len(cast))
	copy(sorted, cast)
	sort.SliceStable(sorted, func(i, j int) bool {
		return billingOrder(sorted[i]) < billingOrder(sorted[j])
	})
	if len(sorted) > CastLimit {
		sorted = sorted[:CastLimit]
	}
	return sorted
}

// billingOrder reads a cast entry's "order", sorting entries without one last.
func billingOrder(e any) float64 {
	if m, ok := e.(map[string]any); ok {
		if o, ok := m["order"].(float64); ok {
			return o
		}
	}
	return math.MaxFloat64
}

func shownCrew(crew []any) []any {
	kept := make([]any, 0, len(crew))
	for _, e := range crew {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if job, _ := m["job"].(string); shownCrewJobs[job] {
			kept = append(kept, e)
		}
	}
	return kept
}

// pick copies each entry with only the given fields. A field the entry lacks
// stays absent rather than becoming null.
func pick(entries []any, fields []string) []any {
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		kept := make(map[string]any, len(fields))
		for _, f := range fields {
			if val, ok := m[f]; ok {
				kept[f] = val
			}
		}
		out = append(out, kept)
	}
	return out
}
