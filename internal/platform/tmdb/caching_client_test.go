package tmdb_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"klyvi-api/internal/platform/tmdb"
)

type fakeRequester struct {
	calls   atomic.Int64
	started chan struct{}
	block   chan struct{}
	result  map[string]interface{}
	err     error
}

func (f *fakeRequester) TMDBRequest(_ context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error) {
	f.calls.Add(1)
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		<-f.block
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestCachingTMDBClient_CachesRepeatGet(t *testing.T) {
	f := &fakeRequester{result: map[string]interface{}{"ok": true}}
	c := tmdb.NewCachingTMDBClient(f)

	for i := range 3 {
		if _, err := c.TMDBRequest(context.Background(), http.MethodGet, "/movie/550", nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (repeat GET should hit cache)", got)
	}
}

func TestCachingTMDBClient_DistinctEndpointsMiss(t *testing.T) {
	f := &fakeRequester{result: map[string]interface{}{"ok": true}}
	c := tmdb.NewCachingTMDBClient(f)

	_, _ = c.TMDBRequest(context.Background(), http.MethodGet, "/movie/550", nil)
	_, _ = c.TMDBRequest(context.Background(), http.MethodGet, "/movie/551", nil)

	if got := f.calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (distinct endpoints)", got)
	}
}

func TestCachingTMDBClient_DoesNotCacheNonGet(t *testing.T) {
	f := &fakeRequester{result: map[string]interface{}{"ok": true}}
	c := tmdb.NewCachingTMDBClient(f)

	_, _ = c.TMDBRequest(context.Background(), http.MethodPost, "/anything", nil)
	_, _ = c.TMDBRequest(context.Background(), http.MethodPost, "/anything", nil)

	if got := f.calls.Load(); got != 2 {
		t.Errorf("non-GET must always pass through: calls = %d, want 2", got)
	}
}

func TestCachingTMDBClient_DoesNotCacheErrors(t *testing.T) {
	f := &fakeRequester{err: errors.New("upstream down")}
	c := tmdb.NewCachingTMDBClient(f)

	if _, err := c.TMDBRequest(context.Background(), http.MethodGet, "/movie/550", nil); err == nil {
		t.Fatal("expected the upstream error to surface")
	}

	f.err = nil
	f.result = map[string]interface{}{"ok": true}
	if _, err := c.TMDBRequest(context.Background(), http.MethodGet, "/movie/550", nil); err != nil {
		t.Fatalf("retry after error: %v", err)
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("an error must not be cached: calls = %d, want 2", got)
	}
}

// hangUpRequester holds its first call until the caller's context ends, like
// the real client does, and answers every later call.
type hangUpRequester struct {
	calls   atomic.Int64
	started chan struct{}
}

func (f *hangUpRequester) TMDBRequest(ctx context.Context, _, _ string, _ any) (map[string]any, error) {
	if f.calls.Add(1) == 1 {
		close(f.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return map[string]any{"ok": true}, nil
}

func TestCachingTMDBClient_JoinerOutlivesALeaderThatHungUp(t *testing.T) {
	f := &hangUpRequester{started: make(chan struct{})}
	c := tmdb.NewCachingTMDBClient(f)

	leaderCtx, hangUp := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := c.TMDBRequest(leaderCtx, http.MethodGet, "/movie/550", nil)
		leaderErr <- err
	}()
	<-f.started

	type answer struct {
		res map[string]any
		err error
	}
	joiner := make(chan answer, 1)
	go func() {
		res, err := c.TMDBRequest(context.Background(), http.MethodGet, "/movie/550", nil)
		joiner <- answer{res, err}
	}()
	// Let the joiner wait on the leader's fetch. A late joiner starts its own
	// fetch instead, which passes too.
	time.Sleep(50 * time.Millisecond)
	hangUp()

	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Errorf("leader err = %v, want context.Canceled", err)
	}
	got := <-joiner
	if got.err != nil {
		t.Fatalf("joiner err = %v, want the joiner to fetch for itself", got.err)
	}
	if got.res["ok"] != true {
		t.Errorf("joiner result = %+v", got.res)
	}
}

func TestCachingTMDBClient_CollapsesConcurrentMisses(t *testing.T) {
	f := &fakeRequester{
		result:  map[string]interface{}{"ok": true},
		started: make(chan struct{}, 1),
		block:   make(chan struct{}),
	}
	c := tmdb.NewCachingTMDBClient(f)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_, _ = c.TMDBRequest(context.Background(), http.MethodGet, "/movie/550", nil)
		}()
	}

	<-f.started // the leader has entered upstream and is blocked
	close(f.block)
	wg.Wait()

	if got := f.calls.Load(); got != 1 {
		t.Errorf("concurrent misses should collapse to 1 upstream call, got %d", got)
	}
}
