package imports

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// TMDBClient is the search boundary used to resolve a title+year to a TMDB
// movie id. Only the behaviour is needed, per repo convention.
type TMDBClient interface {
	TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// matchRow resolves one parsed row to a TMDB movie id via search, preferring
// an exact year match when the row carries a year. Returns 0 when nothing
// matches — the row is then counted as unmatched, never silently dropped.
func matchRow(ctx context.Context, client TMDBClient, row Row) int {
	endpoint := fmt.Sprintf("/search/movie?query=%s&language=en-US&page=1", url.QueryEscape(row.Title))
	if row.Year > 0 {
		endpoint += "&year=" + strconv.Itoa(row.Year)
	}

	resp, err := client.TMDBRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return 0
	}
	results, ok := resp["results"].([]any)
	if !ok || len(results) == 0 {
		// Retry without the year filter — a wrong year in the export should
		// not lose an otherwise-findable title.
		if row.Year > 0 {
			return matchRow(ctx, client, Row{Title: row.Title})
		}
		return 0
	}

	// Prefer an exact title match among the results; fall back to the first
	// (TMDB orders by relevance).
	for _, r := range results {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if title, _ := m["title"].(string); strings.EqualFold(title, row.Title) {
			if id := intField(m, "id"); id != 0 {
				return id
			}
		}
	}
	if m, ok := results[0].(map[string]any); ok {
		return intField(m, "id")
	}
	return 0
}

// matchShow resolves a show row to a TMDB series id through /search/tv.
// Unlike a film, a show is matched only strongly: the title is the show's
// name or original name, the year (when the row has one) is the year it
// first aired, and no other result fits as well. Anything weaker returns 0,
// so a guess never tracks a season of the wrong show.
func matchShow(ctx context.Context, client TMDBClient, row Row) int {
	title := strings.TrimSpace(row.Title)
	endpoint := fmt.Sprintf("/search/tv?query=%s&language=en-US&page=1", url.QueryEscape(title))
	year := ""
	if row.Year > 0 {
		year = strconv.Itoa(row.Year)
		endpoint += "&first_air_date_year=" + year
	}

	resp, err := client.TMDBRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return 0
	}
	results, _ := resp["results"].([]any)

	match := 0
	for _, r := range results {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		original, _ := m["original_name"].(string)
		if !strings.EqualFold(strings.TrimSpace(name), title) && !strings.EqualFold(strings.TrimSpace(original), title) {
			continue
		}
		if aired, _ := m["first_air_date"].(string); year != "" && !strings.HasPrefix(aired, year) {
			continue
		}
		id := intField(m, "id")
		if id == 0 || id == match {
			continue
		}
		if match != 0 {
			return 0 // two shows fit equally well
		}
		match = id
	}
	return match
}

func intField(m map[string]any, key string) int {
	if f, ok := m[key].(float64); ok {
		return int(f)
	}
	return 0
}
