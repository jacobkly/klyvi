package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"klyvi-api/internal/platform/http/middleware"
)

func TestMaxBodyBytes_RejectsOversizeContentLength(t *testing.T) {
	const limit = 16
	called := false
	h := middleware.MaxBodyBytes(limit)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	body := strings.NewReader(strings.Repeat("x", limit+1))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d, want 413", rec.Code)
	}
	if called {
		t.Error("handler must not run for an oversized body")
	}
}

func TestMaxBodyBytes_AllowsWithinLimit(t *testing.T) {
	const limit = 16
	h := middleware.MaxBodyBytes(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}
