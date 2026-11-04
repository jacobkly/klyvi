package movies_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"klyvi-api/internal/movies"
)

// TMDB error text can name the upstream URL, so it stays in the server log.
func TestCatalogLists_HideUpstreamErrors(t *testing.T) {
	api := movies.NewAPI(movies.NewService(
		&fakeTMDB{err: errors.New("GET https://api.themoviedb.org/3/x: upstream detail")},
		&fakeMovieRepo{},
	))

	routes := map[string]http.HandlerFunc{
		"trending":  api.GetTrendingMovies,
		"upcoming":  api.GetUpcomingMovies,
		"popular":   api.GetPopularMovies,
		"top rated": api.GetTopRatedMovies,
	}
	for name, handler := range routes {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, "/v1/movies/x", nil))

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if body.Error != "internal server error" {
				t.Errorf("error = %q, want the generic message", body.Error)
			}
		})
	}
}
