package router_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"klyvi-api/internal/onboarding"
	"klyvi-api/internal/platform/http/router"
	"klyvi-api/internal/similar"
	"klyvi-api/internal/users"
)

type emptyPool struct{}

func (emptyPool) ListActiveWithCatalog(context.Context, int) ([]onboarding.PoolRow, error) {
	return nil, nil
}

// The onboarding pool is the same curated deck for every visitor, so browsers
// and the CDN may keep it like the rest of the public catalog.
func TestOnboardingPoolIsCacheable(t *testing.T) {
	r := router.New(router.Services{
		Users:      users.NewService(nil),
		Onboarding: onboarding.NewService(emptyPool{}, nil),
		AuthMW:     func(next http.Handler) http.Handler { return next },
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/onboarding/pool", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Header().Get("Cache-Control"), "public, max-age=3600, stale-while-revalidate=86400"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
}

// JSON goes out gzipped to a client that accepts it, and plain to one that
// does not. Detail payloads are mostly repetitive JSON keys, so this is a
// large cut in bytes on the wire.
func TestJSONIsGzippedWhenAccepted(t *testing.T) {
	r := router.New(router.Services{
		Users:      users.NewService(nil),
		Onboarding: onboarding.NewService(emptyPool{}, nil),
		AuthMW:     func(next http.Handler) http.Handler { return next },
	})

	tests := []struct {
		name           string
		acceptEncoding string
		wantEncoding   string
	}{
		{"gzip accepted", "gzip, deflate, br", "gzip"},
		{"no encoding accepted", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/pool", nil)
			if tt.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Fatalf("Content-Encoding = %q, want %q", got, tt.wantEncoding)
			}

			var body io.Reader = rec.Body
			if tt.wantEncoding == "gzip" {
				zr, err := gzip.NewReader(rec.Body)
				if err != nil {
					t.Fatalf("gzip reader: %v", err)
				}
				body = zr
			}
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			if err := json.NewDecoder(body).Decode(&envelope); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if envelope.Data == nil {
				t.Error("response has no data field")
			}
		})
	}
}

// noCatalog has nothing cached, so a similar shelf comes from TMDB's list.
type noCatalog struct{}

func (noCatalog) Source(context.Context, int) (*similar.Source, error) { return nil, nil }

func (noCatalog) Candidates(context.Context, *similar.Source, int, int) ([]similar.Candidate, error) {
	return nil, nil
}

func (noCatalog) Resolve(context.Context, []int) (map[int]similar.Resolved, error) { return nil, nil }

func (noCatalog) ByTMDBIDs(context.Context, []int, int) ([]similar.Candidate, error) { return nil, nil }

type oneTitleTMDB struct{}

func (oneTitleTMDB) TMDBRequest(context.Context, string, string, interface{}) (map[string]interface{}, error) {
	return map[string]interface{}{"results": []interface{}{
		map[string]interface{}{"id": float64(5), "title": "A Film"},
	}}, nil
}

// Similar shelves are public catalog: mounted under /movies and /tv and
// cached like the rest of it.
func TestSimilarIsPublicAndCacheable(t *testing.T) {
	ensure := func(context.Context, int) error { return nil }
	r := router.New(router.Services{
		Users:         users.NewService(nil),
		SimilarMovies: similar.NewMovieService(noCatalog{}, ensure, oneTitleTMDB{}, nil, similar.Config{}),
		SimilarTV:     similar.NewTVService(noCatalog{}, ensure, oneTitleTMDB{}, nil, similar.Config{}),
		AuthMW: func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
		},
	})

	for _, path := range []string{"/v1/movies/1/similar", "/v1/tv/1/similar?limit=3"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", path, rec.Code, rec.Body)
		}
		if got, want := rec.Header().Get("Cache-Control"), "public, max-age=3600, stale-while-revalidate=86400"; got != want {
			t.Errorf("%s: Cache-Control = %q, want %q", path, got, want)
		}
	}
}

// Notices are personal, so both routes sit behind the auth middleware.
func TestNotificationRoutesRequireAuth(t *testing.T) {
	r := router.New(router.Services{
		Users: users.NewService(nil),
		AuthMW: func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
		},
	})

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/notifications"},
		{http.MethodPost, "/v1/notifications/read"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

// TMDB's own recommendation lists stopped being served when "More like this"
// moved to Klyvi's ranking, so these paths must stay gone.
func TestTMDBRecommendationRoutesAreGone(t *testing.T) {
	r := router.New(router.Services{
		Users:  users.NewService(nil),
		AuthMW: func(next http.Handler) http.Handler { return next },
	})

	for _, path := range []string{
		"/v1/movies/550/recommendations",
		"/v1/tv/1396/recommendations?type=external",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
}

type downDB struct{}

func (downDB) PingContext(context.Context) error { return context.DeadlineExceeded }

// The uptime monitor polls /health/ready on the root router, outside the
// per-IP limiter, and gets 503 when the database is down.
func TestReadinessIsMountedAndUnthrottled(t *testing.T) {
	r := router.New(router.Services{
		Users:          users.NewService(nil),
		Onboarding:     onboarding.NewService(emptyPool{}, nil),
		AuthMW:         func(next http.Handler) http.Handler { return next },
		DB:             downDB{},
		RateLimitRPM:   1,
		RateLimitBurst: 1,
	})
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d: status %d, want 503", i+1, rec.Code)
		}
	}
}
