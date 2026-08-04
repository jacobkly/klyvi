package tmdb_test

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"klyvi-api/internal/platform/tmdb"
)

type fakeRequester struct {
	calls   atomic.Int64
	started chan struct{}
	block   chan struct{}
	result  map[string]interface{}
	err     error
}

func (f *fakeRequester) TMDBRequest(method, endpoint string, body interface{}) (map[string]interface{}, error) {
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
		if _, err := c.TMDBRequest(http.MethodGet, "/movie/550", nil); err != nil {
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

	_, _ = c.TMDBRequest(http.MethodGet, "/movie/550", nil)
	_, _ = c.TMDBRequest(http.MethodGet, "/movie/551", nil)

	if got := f.calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (distinct endpoints)", got)
	}
}

func TestCachingTMDBClient_DoesNotCacheNonGet(t *testing.T) {
	f := &fakeRequester{result: map[string]interface{}{"ok": true}}
	c := tmdb.NewCachingTMDBClient(f)

	_, _ = c.TMDBRequest(http.MethodPost, "/anything", nil)
	_, _ = c.TMDBRequest(http.MethodPost, "/anything", nil)

	if got := f.calls.Load(); got != 2 {
		t.Errorf("non-GET must always pass through: calls = %d, want 2", got)
	}
}

func TestCachingTMDBClient_DoesNotCacheErrors(t *testing.T) {
	f := &fakeRequester{err: errors.New("upstream down")}
	c := tmdb.NewCachingTMDBClient(f)

	if _, err := c.TMDBRequest(http.MethodGet, "/movie/550", nil); err == nil {
		t.Fatal("expected the upstream error to surface")
	}

	f.err = nil
	f.result = map[string]interface{}{"ok": true}
	if _, err := c.TMDBRequest(http.MethodGet, "/movie/550", nil); err != nil {
		t.Fatalf("retry after error: %v", err)
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("an error must not be cached: calls = %d, want 2", got)
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
			_, _ = c.TMDBRequest(http.MethodGet, "/movie/550", nil)
		}()
	}

	<-f.started // the leader has entered upstream and is blocked
	close(f.block)
	wg.Wait()

	if got := f.calls.Load(); got != 1 {
		t.Errorf("concurrent misses should collapse to 1 upstream call, got %d", got)
	}
}
