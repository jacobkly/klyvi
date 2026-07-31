package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"klyvi-api/internal/platform/http/middleware"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func doGet(t *testing.T, h http.Handler, remoteAddr string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/users/username-available?username=jacob", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// The burst is what type-ahead actually consumes: a few keystrokes in quick
// succession must all get through, and the run after that must not.
func TestRateLimit_AllowsBurstThenRejects(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             3,
	})(okHandler())

	for i := range 3 {
		if code := doGet(t, h, "203.0.113.10:5000"); code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, code)
		}
	}
	if code := doGet(t, h, "203.0.113.10:5000"); code != http.StatusTooManyRequests {
		t.Errorf("request past burst: got %d, want 429", code)
	}
}

// A noisy client must not be able to lock anyone else out, which is the whole
// reason the bucket is keyed per IP rather than shared.
func TestRateLimit_IsolatesClientsByIP(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             2,
	})(okHandler())

	for range 3 {
		doGet(t, h, "203.0.113.10:5000")
	}
	if code := doGet(t, h, "203.0.113.10:5000"); code != http.StatusTooManyRequests {
		t.Fatalf("noisy client should be limited, got %d", code)
	}

	if code := doGet(t, h, "203.0.113.99:5000"); code != http.StatusOK {
		t.Errorf("second client got %d, want 200", code)
	}
}

// A port-only difference is the same caller. Keying on host:port would hand a
// fresh bucket to every new TCP connection and limit nothing at all.
func TestRateLimit_IgnoresSourcePort(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             1,
	})(okHandler())

	if code := doGet(t, h, "203.0.113.10:5000"); code != http.StatusOK {
		t.Fatalf("first request got %d, want 200", code)
	}
	if code := doGet(t, h, "203.0.113.10:6001"); code != http.StatusTooManyRequests {
		t.Errorf("same IP on a new port got %d, want 429", code)
	}
}

func TestRateLimit_DefaultsWhenUnconfigured(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{})(okHandler())
	if code := doGet(t, h, "203.0.113.10:5000"); code != http.StatusOK {
		t.Errorf("zero-value config should still serve, got %d", code)
	}
}
