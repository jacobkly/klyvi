package similar_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/similar"
)

func serve(t *testing.T, svc *similar.Service, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/movies/{id}/similar", similar.NewAPI(svc).Get)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHandler(t *testing.T) {
	var pool []similar.Candidate
	for id := 20; id < 20+similar.MaxLimit; id++ {
		pool = append(pool, candidate(id, []int{101}, nil))
	}
	full := func() *similar.Service {
		return newMovieService(&fakeCatalog{source: keywordRichSource(), pool: pool}, &fakeTMDB{}, nil)
	}

	tests := []struct {
		name       string
		svc        *similar.Service
		path       string
		wantStatus int
		wantItems  int
	}{
		{name: "default limit", svc: full(), path: "/movies/1/similar", wantStatus: 200, wantItems: similar.DefaultLimit},
		{name: "explicit limit", svc: full(), path: "/movies/1/similar?limit=5", wantStatus: 200, wantItems: 5},
		{name: "limit past the cap", svc: full(), path: "/movies/1/similar?limit=500", wantStatus: 200, wantItems: similar.MaxLimit},
		{name: "bad id", svc: full(), path: "/movies/abc/similar", wantStatus: 400},
		{name: "zero limit", svc: full(), path: "/movies/1/similar?limit=0", wantStatus: 400},
		{name: "bad limit", svc: full(), path: "/movies/1/similar?limit=lots", wantStatus: 400},
		{
			name:       "unknown title",
			svc:        newMovieService(&fakeCatalog{}, &fakeTMDB{err: &tmdb.StatusError{Status: 404}}, nil),
			path:       "/movies/999/similar",
			wantStatus: 404,
		},
		{
			name:       "upstream down",
			svc:        newMovieService(&fakeCatalog{}, &fakeTMDB{err: errors.New("connection reset")}, nil),
			path:       "/movies/999/similar",
			wantStatus: 500,
		},
		{
			name:       "nothing similar is an empty list",
			svc:        newMovieService(&fakeCatalog{source: keywordRichSource()}, &fakeTMDB{}, nil),
			path:       "/movies/1/similar",
			wantStatus: 200,
			wantItems:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, tt.svc, tt.path)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantStatus != 200 {
				return
			}
			var body struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Data == nil {
				t.Fatalf("data is null, want a list (body %s)", rec.Body)
			}
			if len(body.Data) != tt.wantItems {
				t.Errorf("got %d items, want %d", len(body.Data), tt.wantItems)
			}
		})
	}
}
