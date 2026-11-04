package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"klyvi-api/internal/platform/timing"
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
	result, err := c.TMDBRequest(context.Background(), "GET", "/movie/1", nil)
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
	if _, err := c.TMDBRequest(context.Background(), "GET", "/movie/0", nil); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("expected 1 attempt (no retry on 4xx), got %d", got)
	}
}

// A catalog refresh deletes a title TMDB answers 404 for and backs off on
// 429, so callers need the status without matching on the message.
func TestTMDBRequest_ErrorCarriesTheStatus(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", status)
		}))
		c := newClientForTest("token", srv.URL, 1000, 1000)
		_, err := c.TMDBRequest(context.Background(), "GET", "/movie/1", nil)
		srv.Close()

		if !IsStatus(err, status) {
			t.Errorf("status %d: IsStatus = false for %v", status, err)
		}
		if IsStatus(err, http.StatusOK) {
			t.Errorf("status %d: IsStatus(200) = true", status)
		}
		var se *StatusError
		if !errors.As(err, &se) || se.Status != status {
			t.Errorf("status %d: errors.As gave %+v", status, se)
		}
	}
	if IsStatus(errors.New("TMDb returned status 404"), http.StatusNotFound) {
		t.Error("IsStatus matched a plain error by its text")
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
	if _, err := c.TMDBRequest(context.Background(), "GET", "/movie/1", nil); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := atomic.LoadInt32(&attempts); got != tmdbMaxAttempts {
		t.Errorf("expected exactly %d attempts, got %d", tmdbMaxAttempts, got)
	}
}

func TestTMDBRequest_CancelStopsTheUpstreamCall(t *testing.T) {
	var attempts atomic.Int32
	hungUp := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		select {
		case <-r.Context().Done():
			hungUp <- struct{}{}
		case <-release:
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)

	c := newClientForTest("token", srv.URL, 1000, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := c.TMDBRequest(ctx, "GET", "/movie/1", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("returned after %v, want soon after the cancel", elapsed)
	}
	select {
	case <-hungUp:
	case <-time.After(time.Second):
		t.Error("the upstream request was never abandoned")
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("attempts = %d, want 1 (no retry once the caller is gone)", n)
	}
}

func TestTMDBRequest_CancelDuringBackoffStopsRetrying(t *testing.T) {
	var attempts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		// The caller goes away while this retriable failure is in flight.
		cancel()
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newClientForTest("token", srv.URL, 1000, 1000)
	_, err := c.TMDBRequest(ctx, "GET", "/movie/1", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("attempts = %d, want 1", n)
	}
}

func TestTMDBRequest_CancelWhileWaitingOnTheLimiter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// One token per second: the first call spends it, the second waits.
	c := newClientForTest("token", srv.URL, 1, 1)
	if _, err := c.TMDBRequest(context.Background(), "GET", "/x", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.TMDBRequest(ctx, "GET", "/x", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("returned after %v, want soon after the cancel", elapsed)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("attempts = %d, want 1 (the cancelled call never reached TMDB)", n)
	}
}

func TestTMDBRequest_HungAttemptIsRetried(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()
	defer close(release)

	c := newClientForTest("token", srv.URL, 1000, 1000)
	c.http.Timeout = 50 * time.Millisecond
	result, err := c.TMDBRequest(context.Background(), "GET", "/movie/1", nil)
	if err != nil {
		t.Fatalf("expected the second attempt to succeed, got %v", err)
	}
	if result["ok"] != true {
		t.Errorf("unexpected body: %+v", result)
	}
	if n := attempts.Load(); n != 2 {
		t.Errorf("attempts = %d, want 2", n)
	}
}

func TestTMDBRequest_HungUpstreamIsNotTheCallersDeadline(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	c := newClientForTest("token", srv.URL, 1000, 1000)
	c.http.Timeout = 30 * time.Millisecond
	_, err := c.TMDBRequest(context.Background(), "GET", "/movie/1", nil)
	if err == nil {
		t.Fatal("expected an error from a TMDB that never answers")
	}
	// Callers that share a fetch retry when the fetch's own caller went away.
	// A slow TMDB must not look like that, or they would retry into it.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, reads as the caller's context ending", err)
	}
	if n := attempts.Load(); n != tmdbMaxAttempts {
		t.Errorf("attempts = %d, want %d", n, tmdbMaxAttempts)
	}
}

func TestRetryChainFitsTheServerWriteTimeout(t *testing.T) {
	// SERVER_TIMEOUT_WRITE in .env.example. TMDB work past it answers a
	// response the server has already given up on.
	const writeTimeout = 5 * time.Second

	worst := tmdbMaxAttempts * tmdbAttemptTimeout
	for a := 1; a < tmdbMaxAttempts; a++ {
		worst += backoff(a)
	}
	if worst >= writeTimeout {
		t.Errorf("worst-case retry chain = %v, want under %v", worst, writeTimeout)
	}
	if got := NewClient("token").http.Timeout; got != tmdbAttemptTimeout {
		t.Errorf("per-attempt timeout = %v, want %v", got, tmdbAttemptTimeout)
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
		if _, err := c.TMDBRequest(context.Background(), "GET", "/x", nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond {
		t.Errorf("expected rate limiting to delay 6 calls past ~400ms, got %v", elapsed)
	}
}

// Every attempt that reaches TMDB is a call, so a page slowed by retries shows
// them in the request log.
func TestTMDBRequest_RecordsEachUpstreamAttempt(t *testing.T) {
	cases := []struct {
		name     string
		failures int32
		want     int
	}{
		{name: "one attempt", failures: 0, want: 1},
		{name: "retries count too", failures: 2, want: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Long enough to register on Windows' coarse clock.
				time.Sleep(20 * time.Millisecond)
				if atomic.AddInt32(&attempts, 1) <= tc.failures {
					http.Error(w, "boom", http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(`{"ok": true}`))
			}))
			defer srv.Close()

			ctx, rec := timing.NewContext(context.Background())
			c := newClientForTest("token", srv.URL, 1000, 1000)
			if _, err := c.TMDBRequest(ctx, "GET", "/movie/1", nil); err != nil {
				t.Fatalf("TMDBRequest: %v", err)
			}

			snap := rec.Snapshot()
			if snap.TMDBCalls != tc.want {
				t.Errorf("TMDBCalls = %d, want %d", snap.TMDBCalls, tc.want)
			}
			if snap.TMDB <= 0 {
				t.Errorf("TMDB duration = %v, want > 0", snap.TMDB)
			}
		})
	}
}

// A request without a recorder, like a seed run, still works.
func TestTMDBRequest_WorksWithoutARecorder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	c := newClientForTest("token", srv.URL, 1000, 1000)
	if _, err := c.TMDBRequest(context.Background(), "GET", "/movie/1", nil); err != nil {
		t.Fatalf("TMDBRequest: %v", err)
	}
}
