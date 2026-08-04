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

// A 429 should tell the client when to come back, per RFC 6585.
func TestRateLimit_SetsRetryAfterOnReject(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             1,
	})(okHandler())

	doGet(t, h, "203.0.113.50:5000") // spend the only token

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.50:5000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header on 429, got none")
	}
}

func doGetCF(t *testing.T, h http.Handler, remoteAddr, cfIP string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/rankings/movies", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("CF-Connecting-IP", cfIP)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// Behind the tunnel every request shares cloudflared's RemoteAddr, so the limiter
// must key on CF-Connecting-IP — otherwise it throttles all clients as one and
// lets a single client evade the cap by reconnecting on a new source port.
func TestRateLimit_KeysOnCFConnectingIPBehindTunnel(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             1,
	})(okHandler())

	const tunnelPeer = "172.18.0.5" // cloudflared; identical for every real client

	// One real client spends its single token, then is limited even though its
	// next request arrives on a different tunnel source port.
	if code := doGetCF(t, h, tunnelPeer+":4001", "203.0.113.10"); code != http.StatusOK {
		t.Fatalf("first request from client A got %d, want 200", code)
	}
	if code := doGetCF(t, h, tunnelPeer+":4002", "203.0.113.10"); code != http.StatusTooManyRequests {
		t.Errorf("second request from client A got %d, want 429", code)
	}

	// A different real client over the same tunnel peer is unaffected.
	if code := doGetCF(t, h, tunnelPeer+":4003", "203.0.113.99"); code != http.StatusOK {
		t.Errorf("client B got %d, want 200", code)
	}
}
