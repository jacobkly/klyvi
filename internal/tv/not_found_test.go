package tv_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tv"
)

// A series or season TMDB doesn't know is a 404, so the web shows its
// not-found page. A TMDB failure is still the server's fault.
func TestGetTvById_UnknownIDIsNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)

	const tvID = 2_100_000_076
	resetShow(t, dbh, tvID)
	tests := []struct {
		name       string
		tmdbStatus int
		path       string
		want       int
	}{
		{"unknown series", http.StatusNotFound, "/tv/2100000076?type=external", http.StatusNotFound},
		{"unknown season", http.StatusNotFound, "/tv/2100000076?type=external&season_num=1", http.StatusNotFound},
		{"tmdb outage", http.StatusServiceUnavailable, "/tv/2100000076?type=external", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &failingTMDB{err: &tmdb.StatusError{Status: tt.tmdbStatus}}
			r := chi.NewRouter()
			r.Get("/tv/{id}", tv.NewAPI(tv.NewService(client, tv.NewRepository(dbh))).GetTvById)

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body)
			}
		})
	}
}
