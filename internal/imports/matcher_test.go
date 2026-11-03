package imports

import (
	"context"
	"net/url"
	"sync"
	"testing"
)

// searchTMDB answers a search with the canned results for its query, and
// records every endpoint it is sent.
type searchTMDB struct {
	results map[string][]any

	mu    sync.Mutex
	calls []string
}

func (s *searchTMDB) TMDBRequest(_ context.Context, _, endpoint string, _ interface{}) (map[string]interface{}, error) {
	s.mu.Lock()
	s.calls = append(s.calls, endpoint)
	s.mu.Unlock()

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"results": s.results[u.Query().Get("query")]}, nil
}

func (s *searchTMDB) endpoints() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// tvResult is one /search/tv result, with numbers as float64 the way the
// client decodes them.
func tvResult(id int, name, originalName, firstAirDate string) map[string]any {
	return map[string]any{
		"id":             float64(id),
		"name":           name,
		"original_name":  originalName,
		"first_air_date": firstAirDate,
	}
}

// A show is tracked only on a strong match: the title is the show's name,
// the year (when the row has one) is the year it first aired, and no other
// result fits as well.
func TestMatchShow(t *testing.T) {
	severance := tvResult(95396, "Severance", "Severance", "2022-02-17")
	officeUK := tvResult(2996, "The Office", "The Office", "2001-07-09")
	officeUS := tvResult(2316, "The Office", "The Office", "2005-03-24")
	money := tvResult(71446, "Money Heist", "La casa de papel", "2017-05-02")

	tests := []struct {
		name    string
		row     Row
		results map[string][]any
		want    int
	}{
		{
			name:    "the name and year match",
			row:     Row{Title: "Severance", Year: 2022},
			results: map[string][]any{"Severance": {severance}},
			want:    95396,
		},
		{
			name:    "case and spacing do not matter",
			row:     Row{Title: " severance ", Year: 2022},
			results: map[string][]any{"severance": {severance}},
			want:    95396,
		},
		{
			name:    "the original name matches",
			row:     Row{Title: "La casa de papel", Year: 2017},
			results: map[string][]any{"La casa de papel": {money}},
			want:    71446,
		},
		{
			name:    "a name with no year, and only one show by it",
			row:     Row{Title: "Severance"},
			results: map[string][]any{"Severance": {severance}},
			want:    95396,
		},
		{
			name:    "a near-miss name is weak",
			row:     Row{Title: "Severence", Year: 2022},
			results: map[string][]any{"Severence": {severance}},
		},
		{
			name:    "a different first-air year is weak",
			row:     Row{Title: "Severance", Year: 2019},
			results: map[string][]any{"Severance": {severance}},
		},
		{
			name:    "two shows share the name and there is no year",
			row:     Row{Title: "The Office"},
			results: map[string][]any{"The Office": {officeUK, officeUS}},
		},
		{
			name:    "the year settles two shows with one name",
			row:     Row{Title: "The Office", Year: 2005},
			results: map[string][]any{"The Office": {officeUK, officeUS}},
			want:    2316,
		},
		{
			name: "nothing found",
			row:  Row{Title: "Zqxwv", Year: 2020},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &searchTMDB{results: tt.results}
			if got := matchShow(context.Background(), client, tt.row); got != tt.want {
				t.Errorf("matchShow = %d, want %d (searched %v)", got, tt.want, client.endpoints())
			}
		})
	}
}
