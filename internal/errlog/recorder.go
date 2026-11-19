package errlog

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// store is where a Recorder writes. It reports whether the event was kept.
type store interface {
	Record(ctx context.Context, e Event) (bool, error)
}

// queueSize bounds the events waiting to be written. Past it, new events
// are dropped: a burst of errors must never slow or block a request.
const queueSize = 256

// writeTimeout bounds one write, so a slow database can't wedge the worker.
const writeTimeout = 5 * time.Second

// Recorder writes events to a store from one background goroutine.
type Recorder struct {
	store   store
	queue   chan Event
	done    chan struct{}
	mu      sync.RWMutex
	closed  bool
	dropped atomic.Int64
}

// NewRecorder starts a recorder over s. Close it on shutdown to flush.
func NewRecorder(s store) *Recorder {
	r := &Recorder{store: s, queue: make(chan Event, queueSize), done: make(chan struct{})}
	go r.run()
	return r
}

func (r *Recorder) run() {
	defer close(r.done)
	for e := range r.queue {
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		if _, err := r.store.Record(ctx, e); err != nil {
			slog.Error("errlog: record failed", "kind", e.Kind, "route", e.Route, "error", err)
		}
		cancel()
	}
}

// Report queues e. It never blocks: when the queue is full, or the recorder
// is closed, the event is dropped.
func (r *Recorder) Report(e Event) {
	if r == nil {
		return
	}
	e = e.clean()
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	select {
	case r.queue <- e:
	default:
		if n := r.dropped.Add(1); n == 1 || n%100 == 0 {
			slog.Warn("errlog: queue full, dropping events", "dropped", n)
		}
	}
}

// Close stops taking events and waits for the queued ones to be written, or
// for ctx to end.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.queue)
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	defaultMu  sync.RWMutex
	defaultRec *Recorder
)

// SetDefault makes r the recorder that Report, Recovered and the middleware
// use. Nil turns reporting off.
func SetDefault(r *Recorder) {
	defaultMu.Lock()
	defaultRec = r
	defaultMu.Unlock()
}

func current() *Recorder {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultRec
}

// Report queues e on the default recorder, if there is one.
func Report(e Event) {
	current().Report(e)
}

// Recovered reports a panic recovered outside a request, such as in a
// background job. what names the job and becomes the event's route.
func Recovered(what string, rec any, stack []byte) {
	e := PanicEvent(rec, stack)
	e.Route = what
	Report(e)
}
