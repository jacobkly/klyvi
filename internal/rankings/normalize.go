package rankings

import "strconv"

// mediaKind selects the TMDB field set and genre table.
type mediaKind int

const (
	mediaMovie mediaKind = iota
	mediaTV
)

// maxRankings is the "Top 100" cap the client promises.
const maxRankings = 100

// Ranking is one entry in a top list, normalised from TMDB.
type Ranking struct {
	Rank       int      `json:"rank"`
	TMDBID     int      `json:"tmdb_id"`
	MediaID    *int     `json:"media_id"` // internal id when the item is cached, else null
	Title      string   `json:"title"`
	Year       int      `json:"year"` // 0 when unknown
	PosterPath string   `json:"poster_path"`
	Genres     []string `json:"genres"`
	Score      float64  `json:"score"` // TMDB vote_average
}

// normalizeRankings turns raw TMDB list results into ranked entries: dedup by
// tmdb id (a title can repeat across pages), map genre ids to names, cap at
// 100, and assign 1-based ranks in arrival order. resolved maps tmdb id →
// internal media_id for the items already in the local catalog; absent ids
// get a null media_id.
func normalizeRankings(results []map[string]any, kind mediaKind, resolved map[int]int) []Ranking {
	genres := movieGenres
	if kind == mediaTV {
		genres = tvGenres
	}

	seen := make(map[int]bool)
	out := make([]Ranking, 0, maxRankings)

	for _, r := range results {
		if len(out) == maxRankings {
			break
		}
		id := intField(r, "id")
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true

		title := stringField(r, "title")
		date := stringField(r, "release_date")
		if kind == mediaTV {
			title = stringField(r, "name")
			date = stringField(r, "first_air_date")
		}

		entry := Ranking{
			Rank:       len(out) + 1,
			TMDBID:     id,
			Title:      title,
			Year:       yearFromDate(date),
			PosterPath: stringField(r, "poster_path"),
			Genres:     genreNames(r, genres),
			Score:      floatField(r, "vote_average"),
		}
		if mid, ok := resolved[id]; ok {
			entry.MediaID = &mid
		}
		out = append(out, entry)
	}
	return out
}

func genreNames(r map[string]any, table map[int]string) []string {
	raw, ok := r["genre_ids"].([]any)
	if !ok {
		return []string{}
	}
	names := make([]string, 0, len(raw))
	for _, g := range raw {
		f, ok := g.(float64)
		if !ok {
			continue
		}
		if name := table[int(f)]; name != "" {
			names = append(names, name)
		}
	}
	return names
}

func intField(r map[string]any, key string) int {
	if f, ok := r[key].(float64); ok {
		return int(f)
	}
	return 0
}

func floatField(r map[string]any, key string) float64 {
	if f, ok := r[key].(float64); ok {
		return f
	}
	return 0
}

func stringField(r map[string]any, key string) string {
	if s, ok := r[key].(string); ok {
		return s
	}
	return ""
}

// yearFromDate pulls the year out of a TMDB "YYYY-MM-DD" date, 0 if absent.
func yearFromDate(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}
