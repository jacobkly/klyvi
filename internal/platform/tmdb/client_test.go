package tmdb

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTMDBRequest_RetriesOn5xxThenSucceeds(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	c := newClientForTest("token", srv.URL, 1000, 1000)
	result, err := c.TMDBRequest("GET", "/movie/1", nil)
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if result["ok"] != true {
		t.Errorf("unexpected body: %+v", result)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("expected 3 attempts (2 failed + 1 ok), got %d", got)
	}
}

func TestTMDBRequest_DoesNotRetryOn4xx(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := newClientForTest("token", srv.URL, 1000, 1000)
	if _, err := c.TMDBRequest("GET", "/movie/0", nil); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("expected 1 attempt (no retry on 4xx), got %d", got)
	}
}

func TestTMDBRequest_GivesUpAfterMaxAttempts(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "perm boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newClientForTest("token", srv.URL, 1000, 1000)
	if _, err := c.TMDBRequest("GET", "/movie/1", nil); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := atomic.LoadInt32(&attempts); got != tmdbMaxAttempts {
		t.Errorf("expected exactly %d attempts, got %d", tmdbMaxAttempts, got)
	}
}

func TestTMDBRequest_RateLimiterThrottles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// Tight limiter for a fast deterministic test: 10 rps with burst 2.
	// 6 calls means the first 2 go immediately and the remaining 4 each wait
	// ~100ms — expected elapsed ≥ ~400ms.
	c := newClientForTest("token", srv.URL, 10, 2)

	const n = 6
	start := time.Now()
	for range n {
		if _, err := c.TMDBRequest("GET", "/x", nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond {
		t.Errorf("expected rate limiting to delay 6 calls past ~400ms, got %v", elapsed)
	}
}
