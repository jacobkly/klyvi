package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"klyvi-api/internal/platform/http/middleware"
)

// slowUpload sends a body in two halves with a pause between them, the way a
// large file arrives over a slow phone connection, and returns the status.
func slowUpload(t *testing.T, url string, pause time.Duration) int {
	t.Helper()
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(strings.Repeat("a", 512)))
		time.Sleep(pause)
		_, _ = pw.Write([]byte(strings.Repeat("b", 512)))
		_ = pw.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, url, pr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.ContentLength = 1024
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// serve runs h behind a server whose read timeout is shorter than the upload,
// with the logging wrapper in front, as in the real router.
func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(middleware.LoggerMiddleware(h))
	srv.Config.ReadTimeout = 150 * time.Millisecond
	srv.Config.WriteTimeout = 150 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

func readAll(w http.ResponseWriter, r *http.Request) {
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		http.Error(w, "read failed", http.StatusRequestTimeout)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func TestLongRequest_LetsASlowUploadFinish(t *testing.T) {
	url := serve(t, middleware.LongRequest(5*time.Second)(http.HandlerFunc(readAll)))
	if got := slowUpload(t, url, 400*time.Millisecond); got != http.StatusOK {
		t.Fatalf("slow upload with a long deadline: status %d, want 200", got)
	}
}

// The control: without the middleware the same upload runs out of time, so
// the test above is proving the deadline, not a lenient server.
func TestLongRequest_WithoutItASlowUploadFails(t *testing.T) {
	url := serve(t, http.HandlerFunc(readAll))
	if got := slowUpload(t, url, 400*time.Millisecond); got == http.StatusOK {
		t.Fatal("slow upload finished without a long deadline, the control is broken")
	}
}
