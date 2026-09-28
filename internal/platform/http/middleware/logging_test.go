package middleware_test

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/timing"
)

// captureLog redirects the standard logger for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

func TestLoggerMiddleware_ServerTimingOnExplicitWriteHeader(t *testing.T) {
	captureLog(t)
	h := middleware.LoggerMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := timing.FromContext(r.Context())
		rec.AddDB(4 * time.Millisecond)
		rec.AddDB(6 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/x", nil))

	got := rr.Header().Get("Server-Timing")
	if !strings.HasPrefix(got, `db;desc="2 queries";dur=10.0, total;dur=`) {
		t.Fatalf("Server-Timing = %q, want the handler's 2 queries and a total", got)
	}
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusCreated)
	}
}

// Most handlers never call WriteHeader; the header must still be set before
// the implicit 200 goes out on the first Write.
func TestLoggerMiddleware_ServerTimingOnImplicitWrite(t *testing.T) {
	captureLog(t)
	h := middleware.LoggerMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timing.FromContext(r.Context()).AddDB(time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/x", nil))

	if got := rr.Header().Get("Server-Timing"); !strings.HasPrefix(got, `db;desc="1 query";dur=1.0, total;dur=`) {
		t.Fatalf("Server-Timing = %q, want 1 query and a total", got)
	}
	if rr.Body.String() != "ok" {
		t.Fatalf("body = %q, want %q", rr.Body.String(), "ok")
	}
}

func TestLoggerMiddleware_LogsTimingFields(t *testing.T) {
	buf := captureLog(t)
	h := middleware.LoggerMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := timing.FromContext(r.Context())
		rec.AddDB(3 * time.Millisecond)
		rec.AddTMDB(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/x", nil))

	out := buf.String()
	for _, want := range []string{"status=200", "db_queries=1", "db_ms=3", "tmdb_calls=1", "tmdb_ms=20"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q:\n%s", want, out)
		}
	}
}
