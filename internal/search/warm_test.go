package search_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/search"
)

type cannedTMDB struct{ resp map[string]any }

func (c cannedTMDB) TMDBRequest(_ context.Context, _, _ string, _ any) (map[string]any, error) {
	return c.resp, nil
}

// gatedRepo holds every cache write until release is closed, and records
// what it was asked to write.
type gatedRepo struct {
	release chan struct{}

	mu      sync.Mutex
	batches [][]int
	indexed [][]int
}

func (r *gatedRepo) InsertMovies(_ context.Context, ms []*movies.Movie) error {
	<-r.release
	ids := make([]int, len(ms))
	for i, m := range ms {
		ids[i] = m.MovieID
	}
	r.mu.Lock()
	r.batches = append(r.batches, ids)
	r.mu.Unlock()
	return nil
}

func (r *gatedRepo) EnsureMediaIndexes(_ context.Context, ids []int) error {
	r.mu.Lock()
	r.indexed = append(r.indexed, ids)
	r.mu.Unlock()
	return nil
}

func hit(id int, mediaType string) map[string]any {
	return map[string]any{"id": float64(id), "media_type": mediaType, "title": "t"}
}

// Search must answer without waiting on the movies cache, then warm every
// movie hit in one batch.
func TestSearch_WarmsCacheOffTheRequestPathInOneBatch(t *testing.T) {
	client := cannedTMDB{resp: map[string]any{"results": []any{
		hit(1, "movie"), hit(2, "movie"), hit(90, "tv"), hit(1, "movie"), hit(7, "person"),
	}}}
	repo := &gatedRepo{release: make(chan struct{})}
	svc := search.NewService(client, repo)

	done := make(chan error, 1)
	go func() {
		_, err := svc.GetSearchResult(context.Background(), "", "anything")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GetSearchResult: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(repo.release)
		t.Fatal("search blocked on the movies cache write")
	}

	close(repo.release)
	svc.Wait()

	if len(repo.batches) != 1 || !slices.Equal(repo.batches[0], []int{1, 2}) {
		t.Errorf("InsertMovies batches = %v, want one batch [1 2]", repo.batches)
	}
	if len(repo.indexed) != 1 || !slices.Equal(repo.indexed[0], []int{1, 2}) {
		t.Errorf("EnsureMediaIndexes batches = %v, want one batch [1 2]", repo.indexed)
	}
}

// TV and person searches carry no movie hits, so they must not write.
func TestSearch_NonMovieSearchesDoNotWarm(t *testing.T) {
	client := cannedTMDB{resp: map[string]any{"results": []any{hit(90, "tv")}}}
	repo := &gatedRepo{release: make(chan struct{})}
	close(repo.release)
	svc := search.NewService(client, repo)

	for _, typ := range []string{"tv", "person", ""} {
		if _, err := svc.GetSearchResult(context.Background(), typ, "anything"); err != nil {
			t.Fatalf("GetSearchResult(%q): %v", typ, err)
		}
	}
	svc.Wait()

	if len(repo.batches) != 0 {
		t.Errorf("InsertMovies batches = %v, want none", repo.batches)
	}
}

// panickingRepo stands in for a bug in the cache write.
type panickingRepo struct{}

func (panickingRepo) InsertMovies(context.Context, []*movies.Movie) error { panic("insert bug") }
func (panickingRepo) EnsureMediaIndexes(context.Context, []int) error     { return nil }

// A panic while warming the cache is contained: search still answers, and an
// unrecovered panic would have ended the API process.
func TestSearch_WarmPanicIsContained(t *testing.T) {
	client := cannedTMDB{resp: map[string]any{"results": []any{hit(1, "movie")}}}
	svc := search.NewService(client, panickingRepo{})
	for range 2 {
		if _, err := svc.GetSearchResult(context.Background(), "", "anything"); err != nil {
			t.Fatalf("GetSearchResult: %v", err)
		}
		svc.Wait()
	}
}
