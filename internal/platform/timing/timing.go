// Package timing collects per-request DB and TMDB timings, so the access log
// and the Server-Timing header can show where a request's time went. The HTTP
// logger puts a Recorder into the request context; the pgx query tracer and
// the TMDB client add to it.
package timing

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Recorder accumulates timings for one request. It is safe for concurrent use,
// and a nil *Recorder is a no-op, so callers never need to check for one.
type Recorder struct {
	mu   sync.Mutex
	snap Snapshot
}

// Snapshot is a point-in-time copy of a Recorder's totals.
type Snapshot struct {
	DBQueries int
	DB        time.Duration
	TMDBCalls int
	TMDB      time.Duration
}

type ctxKey struct{}

// NewContext returns a child context carrying a fresh Recorder.
func NewContext(ctx context.Context) (context.Context, *Recorder) {
	rec := &Recorder{}
	return context.WithValue(ctx, ctxKey{}, rec), rec
}

// FromContext returns the request's Recorder, or nil when there is none.
func FromContext(ctx context.Context) *Recorder {
	rec, _ := ctx.Value(ctxKey{}).(*Recorder)
	return rec
}

// AddDB records one database round trip.
func (r *Recorder) AddDB(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.snap.DBQueries++
	r.snap.DB += d
	r.mu.Unlock()
}

// AddTMDB records one upstream TMDB call.
func (r *Recorder) AddTMDB(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.snap.TMDBCalls++
	r.snap.TMDB += d
	r.mu.Unlock()
}

// Snapshot returns the totals so far.
func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap
}

// ServerTiming renders the snapshot as a Server-Timing header value, with
// total as the request's elapsed time so far.
func (s Snapshot) ServerTiming(total time.Duration) string {
	var parts []string
	if s.DBQueries > 0 {
		parts = append(parts, fmt.Sprintf(`db;desc="%s";dur=%s`, plural(s.DBQueries, "query", "queries"), ms(s.DB)))
	}
	if s.TMDBCalls > 0 {
		parts = append(parts, fmt.Sprintf(`tmdb;desc="%s";dur=%s`, plural(s.TMDBCalls, "call", "calls"), ms(s.TMDB)))
	}
	parts = append(parts, "total;dur="+ms(total))
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond))
}
