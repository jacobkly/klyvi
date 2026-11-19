package rankings

import (
	"context"
	"strings"
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
		map[string]any{"id": float64(238), "title": "The Godfather", "vote_average": 8.7, "vote_count": float64(20000)},
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

// poolTMDB serves one fixed pool on page 1 and nothing after, recording the
// endpoints asked for.
type poolTMDB struct {
	mu        sync.Mutex
	endpoints []string
	pool      []any
}

func (p *poolTMDB) TMDBRequest(_ context.Context, _, endpoint string, _ any) (map[string]any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.endpoints = append(p.endpoints, endpoint)
	if len(p.endpoints) == 1 {
		return map[string]any{"results": p.pool}, nil
	}
	return map[string]any{"results": []any{}}, nil
}

func film(id int, title string, avg float64, votes int, genres ...int) map[string]any {
	g := make([]any, len(genres))
	for i, x := range genres {
		g[i] = float64(x)
	}
	return map[string]any{
		"id": float64(id), "title": title, "name": title,
		"vote_average": avg, "vote_count": float64(votes), "genre_ids": g,
	}
}

func TestMovies_WeightsByVotesAndDropsThinOnes(t *testing.T) {
	tmdb := &poolTMDB{pool: []any{
		film(1, "Brand new and loved", 9.0, 40),
		film(2, "Small hit", 8.9, 2100),
		film(3, "Fine film", 7.6, 9000),
		film(238, "The Godfather", 8.7, 20000),
		film(4, "Middling", 7.0, 5000),
	}}
	got, err := NewService(tmdb, nil).Movies(context.Background())
	if err != nil {
		t.Fatalf("Movies: %v", err)
	}
	for _, r := range got {
		if r.TMDBID == 1 {
			t.Errorf("a 9.0 with 40 votes is listed at #%d", r.Rank)
		}
	}
	if len(got) == 0 || got[0].TMDBID != 238 {
		t.Fatalf("first = %+v, want The Godfather", got)
	}
	if got[0].Rank != 1 || got[0].Score != 8.7 {
		t.Errorf("first = rank %d score %v, want rank 1 and TMDB's own 8.7", got[0].Rank, got[0].Score)
	}
	if len(tmdb.endpoints) != pagesToFetch {
		t.Errorf("fetched %d pages, want %d", len(tmdb.endpoints), pagesToFetch)
	}
	if !strings.HasPrefix(tmdb.endpoints[0], "/discover/movie?") {
		t.Errorf("first endpoint = %q, want TMDB's discover list", tmdb.endpoints[0])
	}
}

func TestTV_LeavesOutTalkAndThinShows(t *testing.T) {
	tmdb := &poolTMDB{pool: []any{
		film(10, "A talk show", 8.9, 3000, 10767),
		film(11, "Breaking Bad", 8.9, 15000, 18, 80),
		film(12, "Niche gem", 8.8, 120, 18),
	}}
	got, err := NewService(tmdb, nil).TV(context.Background())
	if err != nil {
		t.Fatalf("TV: %v", err)
	}
	if len(got) != 1 || got[0].TMDBID != 11 {
		t.Errorf("got %+v, want Breaking Bad alone", got)
	}
	if !strings.HasPrefix(tmdb.endpoints[0], "/discover/tv?") {
		t.Errorf("first endpoint = %q, want TMDB's discover list", tmdb.endpoints[0])
	}
}
