package errlog

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type ctxKey struct{}

// requestState is shared down the middleware chain, so the auth step can
// name the user and the recoverer can say a panic was already recorded.
type requestState struct {
	mu       sync.Mutex
	user     *uuid.UUID
	recorded bool
}

func stateFrom(ctx context.Context) *requestState {
	s, _ := ctx.Value(ctxKey{}).(*requestState)
	return s
}

// SetUser names the signed-in user for errors on this request.
func SetUser(ctx context.Context, id uuid.UUID) {
	if s := stateFrom(ctx); s != nil {
		s.mu.Lock()
		s.user = &id
		s.mu.Unlock()
	}
}

// RecordPanic reports a panic recovered while serving r, and marks the
// request so the 500 that follows isn't reported again.
func RecordPanic(r *http.Request, rec any, stack []byte) {
	e := PanicEvent(rec, stack)
	e.Method = r.Method
	e.Route = routeOf(r)
	e.Status = http.StatusInternalServerError
	if s := stateFrom(r.Context()); s != nil {
		s.mu.Lock()
		s.recorded = true
		e.UserID = s.user
		s.mu.Unlock()
	}
	Report(e)
}

// routeOf is the matched route pattern, so /v1/movies/1 and /v1/movies/2
// are one route. Before routing it falls back to the path.
func routeOf(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return r.URL.Path
}

// maxCapture is how much of a 5xx body is kept to read its error message.
const maxCapture = 512

// Middleware reports every 5xx response the API sends, with its route,
// method, user and message. Mount it outside the recoverer, so a panic's
// 500 passes through it.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := &requestState{}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, state))
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)

		if sw.status < 500 {
			return
		}
		state.mu.Lock()
		recorded, user := state.recorded, state.user
		state.mu.Unlock()
		if recorded {
			return
		}
		Report(Event{
			Source:  SourceAPI,
			Kind:    strconv.Itoa(sw.status),
			Message: messageOf(decode(sw.body, sw.Header().Get("Content-Encoding")), sw.status),
			Route:   routeOf(r),
			Method:  r.Method,
			Status:  sw.status,
			UserID:  user,
		})
	})
}

// messageOf reads the API's {"error": "..."} body, or falls back to the
// status text.
func messageOf(body []byte, status int) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return http.StatusText(status)
}

// decode undoes the API's response compression, which runs inside this
// middleware. The capture may be cut short, so it keeps what it can read.
func decode(body []byte, encoding string) []byte {
	var rd io.Reader
	switch encoding {
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil
		}
		rd = zr
	case "deflate":
		rd = flate.NewReader(bytes.NewReader(body))
	default:
		return body
	}
	out, _ := io.ReadAll(io.LimitReader(rd, maxCapture*4))
	return out
}

// statusWriter records the status and, for a 5xx, the start of the body.
type statusWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 500 && len(w.body) < maxCapture {
		n := min(len(b), maxCapture-len(w.body))
		w.body = append(w.body, b[:n]...)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
