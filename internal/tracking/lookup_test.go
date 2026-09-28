package tracking_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/timing"
	"klyvi-api/internal/tracking"
)

// oneTracked tracks a single title and finds it only by its exact identity.
type oneTracked struct {
	memTracking
	tmdbID    int
	mediaType string
	season    *int
}

func (r *oneTracked) GetEntryByTMDB(_ context.Context, _ uuid.UUID, tmdbID int, mediaType string, season *int) (*tracking.Entry, error) {
	sameSeason := (season == nil && r.season == nil) || (season != nil && r.season != nil && *season == *r.season)
	if tmdbID != r.tmdbID || mediaType != r.mediaType || !sameSeason {
		return nil, nil
	}
	return &tracking.Entry{MediaID: 7, MediaType: mediaType, TMDBID: &tmdbID, SeasonNumber: season}, nil
}

func TestLookup_Handler(t *testing.T) {
	two := 2
	api := tracking.NewAPI(tracking.NewService(&oneTracked{tmdbID: 1396, mediaType: tracking.MediaTypeSeason, season: &two}))

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"tracked season", "tmdb_id=1396&media_type=season&season_number=2", http.StatusOK},
		{"another season of the show", "tmdb_id=1396&media_type=season&season_number=3", http.StatusNotFound},
		{"untracked movie", "tmdb_id=550&media_type=movie", http.StatusNotFound},
		{"missing tmdb_id", "media_type=movie", http.StatusBadRequest},
		{"non-numeric tmdb_id", "tmdb_id=abc&media_type=movie", http.StatusBadRequest},
		{"zero tmdb_id", "tmdb_id=0&media_type=movie", http.StatusBadRequest},
		{"unknown media_type", "tmdb_id=1396&media_type=show", http.StatusBadRequest},
		{"season without a number", "tmdb_id=1396&media_type=season", http.StatusBadRequest},
		{"movie with a season number", "tmdb_id=550&media_type=movie&season_number=1", http.StatusBadRequest},
		{"non-numeric season_number", "tmdb_id=1396&media_type=season&season_number=one", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/tracking/lookup?"+tc.query, nil)
			req = req.WithContext(middleware.WithUserUUID(req.Context(), uuid.New()))
			rec := httptest.NewRecorder()
			api.Lookup(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusOK && !strings.Contains(rec.Body.String(), `"media_id":7`) {
				t.Errorf("body does not carry the entry: %s", rec.Body.String())
			}
		})
	}
}

func TestLookup_HandlerNeedsAUser(t *testing.T) {
	api := tracking.NewAPI(tracking.NewService(&memTracking{}))
	rec := httptest.NewRecorder()
	api.Lookup(rec, httptest.NewRequest("GET", "/v1/tracking/lookup?tmdb_id=550&media_type=movie", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A detail page asks about one title in one query, and only ever sees the
// caller's own entry for exactly that movie or season.
func TestLookup_FindsOneEntryInOneQuery(t *testing.T) {
	env := setupTracking(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	score := 81
	if _, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID: 550, MediaType: tracking.MediaTypeMovie, Score: &score,
	}); err != nil {
		t.Fatalf("add movie: %v", err)
	}
	one := 1
	if _, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
		TMDBID: 1396, MediaType: tracking.MediaTypeSeason, SeasonNumber: &one,
	}); err != nil {
		t.Fatalf("add season: %v", err)
	}

	qctx, rec := timing.NewContext(ctx)
	movie, err := env.service.Lookup(qctx, env.userID, tracking.LookupRequest{TMDBID: 550, MediaType: tracking.MediaTypeMovie})
	if err != nil {
		t.Fatalf("Lookup movie: %v", err)
	}
	if n := rec.Snapshot().DBQueries; n != 1 {
		t.Errorf("lookup made %d queries, want 1", n)
	}
	if movie == nil || movie.Score == nil || *movie.Score != 81 || movie.Title == nil || *movie.Title == "" {
		t.Fatalf("movie entry = %+v, want the scored, titled entry", movie)
	}

	season, err := env.service.Lookup(ctx, env.userID, tracking.LookupRequest{TMDBID: 1396, MediaType: tracking.MediaTypeSeason, SeasonNumber: &one})
	if err != nil {
		t.Fatalf("Lookup season: %v", err)
	}
	if season == nil || season.SeasonNumber == nil || *season.SeasonNumber != 1 {
		t.Fatalf("season entry = %+v, want season 1", season)
	}

	two := 2
	misses := []struct {
		name string
		user uuid.UUID
		req  tracking.LookupRequest
	}{
		{"an untracked season of a tracked show", env.userID, tracking.LookupRequest{TMDBID: 1396, MediaType: tracking.MediaTypeSeason, SeasonNumber: &two}},
		{"a tracked id under the wrong media type", env.userID, tracking.LookupRequest{TMDBID: 1396, MediaType: tracking.MediaTypeMovie}},
		{"another user's entry", uuid.New(), tracking.LookupRequest{TMDBID: 550, MediaType: tracking.MediaTypeMovie}},
	}
	for _, m := range misses {
		got, err := env.service.Lookup(ctx, m.user, m.req)
		if err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if got != nil {
			t.Errorf("%s: got entry %+v, want none", m.name, got)
		}
	}
}
