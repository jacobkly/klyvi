package tracking_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/tracking"
)

// tenEpisodes is a season with ten episodes. It records whether a write got
// through, so a rejected request is proven to store nothing.
type tenEpisodes struct {
	memTracking
	wrote bool
}

func (r *tenEpisodes) EpisodeCount(context.Context, int) (*int, error) {
	n := 10
	return &n, nil
}

func (r *tenEpisodes) AddEntry(ctx context.Context, u uuid.UUID, id int, p tracking.AddParams) (*tracking.Entry, error) {
	r.wrote = true
	return r.memTracking.AddEntry(ctx, u, id, p)
}

func (r *tenEpisodes) UpdateEntry(ctx context.Context, u uuid.UUID, id int, p tracking.UpdatePatch) (*tracking.Entry, error) {
	r.wrote = true
	return r.memTracking.UpdateEntry(ctx, u, id, p)
}

// A score outside 0 to 100, or more episodes than the season has, is a 400
// from any client, so a bad value never reaches the database.
func TestTracking_RejectsOutOfRangeScoresAndEpisodes(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"add score 101", "POST", "/v1/tracking", `{"tmdb_id":550,"media_type":"movie","score":101}`, http.StatusBadRequest},
		{"add score -1", "POST", "/v1/tracking", `{"tmdb_id":550,"media_type":"movie","score":-1}`, http.StatusBadRequest},
		{"add score 100", "POST", "/v1/tracking", `{"tmdb_id":550,"media_type":"movie","score":100}`, http.StatusCreated},
		{"add 11 of 10 episodes", "POST", "/v1/tracking", `{"tmdb_id":95396,"media_type":"season","season_number":1,"episode_progress":11}`, http.StatusBadRequest},
		{"add 10 of 10 episodes", "POST", "/v1/tracking", `{"tmdb_id":95396,"media_type":"season","season_number":1,"episode_progress":10}`, http.StatusCreated},
		{"add negative episodes", "POST", "/v1/tracking", `{"tmdb_id":95396,"media_type":"season","season_number":1,"episode_progress":-1}`, http.StatusBadRequest},
		{"update score 101", "PATCH", "/v1/tracking/7", `{"score":101}`, http.StatusBadRequest},
		{"update 11 of 10 episodes", "PATCH", "/v1/tracking/7", `{"episode_progress":11}`, http.StatusBadRequest},
		{"update 10 of 10 episodes", "PATCH", "/v1/tracking/7", `{"episode_progress":10}`, http.StatusOK},
		{"update negative episodes", "PATCH", "/v1/tracking/7", `{"episode_progress":-3}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &tenEpisodes{}
			api := tracking.NewAPI(tracking.NewService(repo))
			r := chi.NewRouter()
			r.Post("/v1/tracking", api.Add)
			r.Patch("/v1/tracking/{media_id}", api.Update)

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req = req.WithContext(middleware.WithUserUUID(req.Context(), uuid.New()))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusBadRequest && repo.wrote {
				t.Error("a rejected request still wrote to the database")
			}
		})
	}
}
