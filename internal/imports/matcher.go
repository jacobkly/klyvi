package imports

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// TMDBClient is the search boundary used to resolve a title+year to a TMDB
// movie id. Only the behaviour is needed, per repo convention.
type TMDBClient interface {
	TMDBRequest(method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// matchRow resolves one parsed row to a TMDB movie id via search, preferring
// an exact year match when the row carries a year. Returns 0 when nothing
// matches — the row is then counted as unmatched, never silently dropped.
func matchRow(client TMDBClient, row Row) int {
	endpoint := fmt.Sprintf("/search/movie?query=%s&language=en-US&page=1", url.QueryEscape(row.Title))
	if row.Year > 0 {
		endpoint += "&year=" + strconv.Itoa(row.Year)
	}

	resp, err := client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return 0
	}
	results, ok := resp["results"].([]any)
	if !ok || len(results) == 0 {
		// Retry without the year filter — a wrong year in the export should
		// not lose an otherwise-findable title.
		if row.Year > 0 {
			return matchRow(client, Row{Title: row.Title})
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

func intField(m map[string]any, key string) int {
	if f, ok := m[key].(float64); ok {
		return int(f)
	}
	return 0
}
