package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"klyvi-api/internal/platform/http/middleware"
)

func TestRecoverer_TurnsPanicInto500(t *testing.T) {
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := middleware.Recoverer(panicking)

	rec := httptest.NewRecorder()
	// Must not propagate — the middleware absorbs it into a response.
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response is not the JSON error envelope: %v", err)
	}
	if body.Error == "" {
		t.Error("expected an error message in the envelope")
	}
}

// http.ErrAbortHandler is the documented way for a handler to abort a response
// mid-flight; the recoverer must let it propagate, not swallow it into a 500.
func TestRecoverer_PropagatesErrAbortHandler(t *testing.T) {
	aborting := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	h := middleware.Recoverer(aborting)

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("expected ErrAbortHandler to propagate, got %v", rec)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	t.Error("ServeHTTP returned without re-panicking")
}
