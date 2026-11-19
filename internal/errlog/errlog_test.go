package errlog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

func TestScrub(t *testing.T) {
	jwt := "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
	tests := []struct {
		name, in, want string
	}{
		{"plain text stays", "cannot read properties of undefined", "cannot read properties of undefined"},
		{"a JWT", "token " + jwt + " expired", "token [redacted] expired"},
		{"a bearer header", "Authorization: Bearer abc.def-123", "Authorization: Bearer [redacted]"},
		{"a token in a query", "GET /v1/x?access_token=abc123&page=2", "GET /v1/x?access_token=[redacted]&page=2"},
		{"an api key in a query", "fetch /3/movie?api_key=deadbeef failed", "fetch /3/movie?api_key=[redacted] failed"},
		{"an email", "no user jo.doe+x@example.co.uk here", "no user [redacted] here"},
		{"a secret key", "key sb_secret_abcdefghijklmnopqrstu leaked", "key [redacted] leaked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Scrub(tt.in); got != tt.want {
				t.Errorf("Scrub(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEventCleanTrimsLongFields(t *testing.T) {
	e := Event{Source: SourceWeb, Kind: "Error", Message: strings.Repeat("é", maxMessage)}.clean()
	if len(e.Message) > maxMessage {
		t.Fatalf("message is %d bytes, want at most %d", len(e.Message), maxMessage)
	}
	if !strings.HasPrefix(e.Message, "é") || strings.ContainsRune(e.Message, '�') {
		t.Fatal("trimming cut a character in half")
	}
}

// boom and bang panic from two different places.
func boom() { panic("boom") }
func bang() { var m map[string]int; m["x"] = 1 }

func panicStack(f func()) (rec any, stack []byte) {
	defer func() {
		rec = recover()
		stack = debug.Stack()
	}()
	f()
	return nil, nil
}

func TestPanicEventGroupsBySite(t *testing.T) {
	r1, s1 := panicStack(boom)
	r2, s2 := panicStack(boom)
	r3, s3 := panicStack(bang)

	a, b, c := PanicEvent(r1, s1).clean(), PanicEvent(r2, s2).clean(), PanicEvent(r3, s3).clean()
	if a.Fingerprint() != b.Fingerprint() {
		t.Errorf("the same panic twice has two fingerprints: %q and %q", a.Anchor, b.Anchor)
	}
	if a.Fingerprint() == c.Fingerprint() {
		t.Errorf("two different panics share a fingerprint (anchor %q)", a.Anchor)
	}
	if !strings.HasSuffix(a.Anchor, ".boom") || !strings.HasSuffix(c.Anchor, ".bang") {
		t.Errorf("anchors = %q, %q, want the functions that panicked", a.Anchor, c.Anchor)
	}
	if a.Kind != "panic" || a.Message != "boom" {
		t.Errorf("kind, message = %q, %q", a.Kind, a.Message)
	}
}

// fakeStore keeps what it is given, grouped by fingerprint like the table.
type fakeStore struct {
	mu     sync.Mutex
	events []Event
	counts map[string]int
}

func (f *fakeStore) Record(_ context.Context, e Event) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts == nil {
		f.counts = map[string]int{}
	}
	f.events = append(f.events, e)
	f.counts[e.Fingerprint()]++
	return true, nil
}

// withRecorder installs a recorder over a fake store as the default, and
// returns a flush that drains it.
func withRecorder(t *testing.T) (*fakeStore, func()) {
	t.Helper()
	store := &fakeStore{}
	rec := NewRecorder(store)
	SetDefault(rec)
	t.Cleanup(func() { SetDefault(nil) })
	return store, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rec.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// testRouter mounts the middleware the way the API does, with a stand-in for
// the API's Recoverer.
func testRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(Middleware)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					RecordPanic(r, rec, debug.Stack())
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	})
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			SetUser(r.Context(), uuid.MustParse("11111111-1111-1111-1111-111111111111"))
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/v1/things/{id}", func(http.ResponseWriter, *http.Request) { boom() })
	r.Get("/v1/feed", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to load feed"}`))
	})
	r.Get("/v1/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	return r
}

func TestMiddlewareRecordsPanicsOnceAndGroupsThem(t *testing.T) {
	store, flush := withRecorder(t)
	h := testRouter()
	for _, path := range []string{"/v1/things/1", "/v1/things/2"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status %d", path, rr.Code)
		}
	}
	flush()

	if len(store.events) != 2 {
		t.Fatalf("recorded %d events, want 2 (one per panic, none for the 500 after it)", len(store.events))
	}
	e := store.events[0]
	if e.Kind != "panic" || e.Route != "/v1/things/{id}" || e.Method != "GET" || e.Status != 500 {
		t.Errorf("event = %+v", e)
	}
	if e.UserID == nil || e.UserID.String() != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("user = %v, want the signed-in user", e.UserID)
	}
	if store.counts[e.Fingerprint()] != 2 {
		t.Errorf("the same panic twice made %d groups, want 1", len(store.counts))
	}
}

func TestMiddlewareRecords500sWithTheirMessage(t *testing.T) {
	store, flush := withRecorder(t)
	h := testRouter()
	for _, path := range []string{"/v1/feed", "/v1/missing"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	flush()

	if len(store.events) != 1 {
		t.Fatalf("recorded %d events, want only the 500", len(store.events))
	}
	e := store.events[0]
	if e.Kind != "500" || e.Message != "failed to load feed" || e.Route != "/v1/feed" {
		t.Errorf("event = %+v", e)
	}
}

func TestRecoveredReportsBackgroundPanics(t *testing.T) {
	store, flush := withRecorder(t)
	rec, stack := panicStack(boom)
	Recovered("import worker", rec, stack)
	flush()

	if len(store.events) != 1 || store.events[0].Route != "import worker" || store.events[0].Kind != "panic" {
		t.Fatalf("events = %+v", store.events)
	}
}

func TestReportIsANoOpWithoutARecorder(t *testing.T) {
	SetDefault(nil)
	Report(Event{Source: SourceAPI, Kind: "500", Message: "x"}) // must not panic
}

func TestRecorderIgnoresReportsAfterClose(t *testing.T) {
	store := &fakeStore{}
	rec := NewRecorder(store)
	if err := rec.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec.Report(Event{Source: SourceAPI, Kind: "500", Message: "late"}) // must not panic
	if len(store.events) != 0 {
		t.Fatalf("a closed recorder stored %d events", len(store.events))
	}
}

func TestReportHandler(t *testing.T) {
	user := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	signedIn := func(context.Context) (uuid.UUID, bool) { return user, true }
	signedOut := func(context.Context) (uuid.UUID, bool) { return uuid.Nil, false }

	tests := []struct {
		name     string
		body     string
		userFrom func(context.Context) (uuid.UUID, bool)
		status   int
		check    func(t *testing.T, e Event)
	}{
		{
			name:     "a boundary report, signed in",
			body:     `{"kind":"TypeError","message":"x is undefined","stack":"TypeError: x is undefined\n    at Library (https://www.klyvi.net/_next/static/chunks/a.js:1:200)","page":"/library"}`,
			userFrom: signedIn,
			status:   http.StatusAccepted,
			check: func(t *testing.T, e Event) {
				if e.Source != SourceWeb || e.Kind != "TypeError" || e.Route != "/library" {
					t.Errorf("event = %+v", e)
				}
				if e.UserID == nil || *e.UserID != user {
					t.Errorf("user = %v", e.UserID)
				}
				if !strings.Contains(e.Anchor, "at Library") {
					t.Errorf("anchor = %q, want the top frame", e.Anchor)
				}
			},
		},
		{
			name:     "signed out, no stack",
			body:     `{"message":"Failed to fetch","page":"/"}`,
			userFrom: signedOut,
			status:   http.StatusAccepted,
			check: func(t *testing.T, e Event) {
				if e.UserID != nil || e.Kind != "Error" {
					t.Errorf("event = %+v", e)
				}
			},
		},
		{name: "no message", body: `{"kind":"Error","page":"/"}`, userFrom: signedOut, status: http.StatusBadRequest},
		{name: "not JSON", body: `oops`, userFrom: signedOut, status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}
			rec := NewRecorder(store)
			api := NewAPI(rec, tt.userFrom)

			rr := httptest.NewRecorder()
			api.Report(rr, httptest.NewRequest(http.MethodPost, "/v1/errors", strings.NewReader(tt.body)))
			_ = rec.Close(context.Background())

			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, tt.status, rr.Body.String())
			}
			if tt.check == nil {
				if len(store.events) != 0 {
					t.Fatalf("a rejected report was stored")
				}
				return
			}
			var body struct {
				Data struct {
					Received bool `json:"received"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || !body.Data.Received {
				t.Fatalf("body = %s", rr.Body.String())
			}
			if len(store.events) != 1 {
				t.Fatalf("stored %d events, want 1", len(store.events))
			}
			tt.check(t, store.events[0])
		})
	}
}

func TestMiddlewareReadsTheMessageOfACompressed500(t *testing.T) {
	store, flush := withRecorder(t)
	r := chi.NewRouter()
	r.Use(Middleware)
	r.Use(chimw.Compress(5, "application/json"))
	r.Get("/v1/feed", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to load feed"}`))
	})
	for _, enc := range []string{"gzip", "deflate"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/feed", nil)
		req.Header.Set("Accept-Encoding", enc)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Header().Get("Content-Encoding") != enc {
			t.Fatalf("the response wasn't %s encoded, so the test proves nothing", enc)
		}
	}
	flush()

	if len(store.events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(store.events))
	}
	for _, e := range store.events {
		if e.Message != "failed to load feed" {
			t.Errorf("message = %q, want the JSON error", e.Message)
		}
	}
}
