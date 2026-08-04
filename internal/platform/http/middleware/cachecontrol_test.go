package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"klyvi-api/internal/platform/http/middleware"
)

func TestCacheControl_SetsHeaderOnSuccess(t *testing.T) {
	h := middleware.CacheControl(3600, 86400)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := rec.Header().Get("Cache-Control"), "public, max-age=3600, stale-while-revalidate=86400"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
}

func TestCacheControl_OmitsSWRWhenZero(t *testing.T) {
	h := middleware.CacheControl(600, 0)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=600" {
		t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=600")
	}
}

// A transient upstream failure must not be cached at the browser or CDN.
func TestCacheControl_SkipsNonSuccessResponses(t *testing.T) {
	h := middleware.CacheControl(3600, 86400)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("error response should carry no Cache-Control, got %q", got)
	}
}
