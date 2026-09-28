package rankings

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gatedTMDB counts top_rated page fetches and holds each one until released,
// so concurrent misses overlap.
type gatedTMDB struct {
	calls   atomic.Int32
	release chan struct{}
}

func (g *gatedTMDB) TMDBRequest(context.Context, string, string, any) (map[string]any, error) {
	g.calls.Add(1)
	<-g.release
	return map[string]any{"results": []any{
		map[string]any{"id": float64(238), "title": "The Godfather"},
	}}, nil
}

func TestTop_ConcurrentMissesShareOneFetch(t *testing.T) {
	tmdb := &gatedTMDB{release: make(chan struct{})}
	svc := NewService(tmdb, nil)

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := svc.Movies(context.Background())
			if err != nil {
				t.Errorf("Movies: %v", err)
				return
			}
			if len(got) != 1 {
				t.Errorf("got %d rankings, want 1", len(got))
			}
		}()
	}
	// Let every caller miss the cache before the first fetch returns. A late
	// caller finds the cached list instead, so the count holds either way.
	time.Sleep(50 * time.Millisecond)
	close(tmdb.release)
	wg.Wait()

	if n := tmdb.calls.Load(); n != pagesToFetch {
		t.Errorf("TMDB page fetches = %d, want %d (one set)", n, pagesToFetch)
	}
}
