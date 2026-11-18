package movies_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/tmdb"
)

// An id TMDB doesn't know is a 404, so the web shows its not-found page. A
// TMDB failure is still the server's fault.
func TestGetMovieById_UnknownIDIsNotFound(t *testing.T) {
	tests := []struct {
		name       string
		tmdbStatus int
		path       string
		want       int
	}{
		{"unknown id", http.StatusNotFound, "/movies/999999999?id_type=tmdb", http.StatusNotFound},
		{"unknown collection", http.StatusNotFound, "/movies/999999999/collection", http.StatusNotFound},
		{"tmdb outage", http.StatusServiceUnavailable, "/movies/999999999?id_type=tmdb", http.StatusInternalServerError},
		{"tmdb rejects our key", http.StatusUnauthorized, "/movies/999999999?id_type=tmdb", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := movies.NewAPI(movies.NewService(
				&fakeTMDB{err: &tmdb.StatusError{Status: tt.tmdbStatus}},
				&fakeMovieRepo{},
			))
			r := chi.NewRouter()
			r.Get("/movies/{id}", api.GetMovieById)
			r.Get("/movies/{id}/collection", api.GetMovieCollection)

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body)
			}
		})
	}
}
