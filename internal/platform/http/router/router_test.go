package router_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"klyvi-api/internal/errlog"
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

// Every API response tells the browser not to guess its type, and CORS does
// not offer credentials, since the client sends a Bearer token, not cookies.
func TestResponsesAreNosniffAndCORSHasNoCredentials(t *testing.T) {
	r := router.New(router.Services{
		Users:      users.NewService(nil),
		Onboarding: onboarding.NewService(emptyPool{}, nil),
		AuthMW:     func(next http.Handler) http.Handler { return next },
	})

	for _, path := range []string{"/health", "/v1/onboarding/pool", "/v1/no-such-route"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Origin", "http://localhost:3000")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want the origin", path, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("%s: Access-Control-Allow-Credentials = %q, want none", path, got)
		}
	}
}

type keptEvents struct {
	mu     sync.Mutex
	events []errlog.Event
}

func (k *keptEvents) Record(_ context.Context, e errlog.Event) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.events = append(k.events, e)
	return true, nil
}

// The web client reports errors without signing in, and the route is
// limited hard since anyone can call it.
func TestErrorReportsArePublicAndLimited(t *testing.T) {
	store := &keptEvents{}
	rec := errlog.NewRecorder(store)
	r := router.New(router.Services{
		Users:  users.NewService(nil),
		AuthMW: func(http.Handler) http.Handler { return http.NotFoundHandler() },
		Errors: rec,
	})

	codes := []int{}
	for range 6 {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/errors",
			strings.NewReader(`{"kind":"TypeError","message":"x is undefined","page":"/home"}`))
		req.RemoteAddr = "203.0.113.9:1234"
		r.ServeHTTP(rr, req)
		codes = append(codes, rr.Code)
	}
	if err := rec.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	for i, c := range codes[:5] {
		if c != http.StatusAccepted {
			t.Fatalf("report %d: status %d, want 202", i+1, c)
		}
	}
	if codes[5] != http.StatusTooManyRequests {
		t.Errorf("the 6th report in a burst: status %d, want 429", codes[5])
	}
	if len(store.events) != 5 || store.events[0].Route != "/home" {
		t.Errorf("stored %+v", store.events)
	}
}

func TestErrorReportsAreOffWithoutARecorder(t *testing.T) {
	r := router.New(router.Services{
		Users:  users.NewService(nil),
		AuthMW: func(next http.Handler) http.Handler { return next },
	})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/errors", strings.NewReader(`{"message":"x"}`)))
	if rr.Code == http.StatusAccepted {
		t.Fatal("POST /v1/errors is mounted without a recorder")
	}
}
