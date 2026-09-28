package movies_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"klyvi-api/internal/movies"
)

// fakeTMDB answers every request with one canned response, or an error.
type fakeTMDB struct {
	resp  map[string]any
	err   error
	calls int
}

func (f *fakeTMDB) TMDBRequest(_, _ string, _ any) (map[string]any, error) {
	f.calls++
	return f.resp, f.err
}

// fakeMovieRepo keeps one cached row and records what the service writes.
type fakeMovieRepo struct {
	cached   *movies.Movie
	inserted []*movies.Movie
}

func (r *fakeMovieRepo) GetByTMDBID(context.Context, int) (*movies.Movie, error) {
	return r.cached, nil
}
func (r *fakeMovieRepo) GetMovieIDByMediaID(context.Context, int) (int, error) { return 0, nil }
func (r *fakeMovieRepo) InsertMovie(_ context.Context, m *movies.Movie) error {
	r.inserted = append(r.inserted, m)
	return nil
}
func (r *fakeMovieRepo) EnsureMediaIndex(context.Context, int) error { return nil }
func (r *fakeMovieRepo) GetCollectionIDByMovieID(context.Context, int) (int, error) {
	return 0, nil
}
func (r *fakeMovieRepo) GetCollectionByCollectionID(context.Context, int) ([]movies.MovieCollection, error) {
	return nil, nil
}
func (r *fakeMovieRepo) InsertMovieCollectionBatch(context.Context, []movies.MovieCollection) error {
	return nil
}

func rawJSON(s string) *json.RawMessage {
	rm := json.RawMessage(s)
	return &rm
}

func strPtr(s string) *string { return &s }

// fullDetail is a TMDB /movie/{id} response with keywords and credits appended.
func fullDetail(id int) map[string]any {
	return map[string]any{
		"id":       float64(id),
		"title":    "Fight Club",
		"genres":   []any{map[string]any{"id": float64(18), "name": "Drama"}},
		"keywords": map[string]any{"keywords": []any{map[string]any{"id": float64(825), "name": "support group"}}},
		"credits":  map[string]any{"cast": []any{}, "crew": []any{}},
	}
}

func TestGetMovieById_CachedRows(t *testing.T) {
	// A search hit is cached without keywords or credits, because TMDB's
	// search payload carries neither.
	skinny := &movies.Movie{MovieID: 550, Title: strPtr("Fight Club")}
	rich := &movies.Movie{
		MovieID:  550,
		Title:    strPtr("Fight Club"),
		Keywords: rawJSON(`[{"id":825,"name":"support group"}]`),
		Credits:  rawJSON(`{"cast":[],"crew":[]}`),
	}

	tests := []struct {
		name          string
		cached        *movies.Movie
		tmdbErr       error
		wantTMDBCalls int
		wantInserts   int
		wantKeywords  bool
	}{
		{name: "rich row is served from the cache", cached: rich, wantTMDBCalls: 0, wantInserts: 0, wantKeywords: true},
		{name: "skinny row is upgraded from TMDB", cached: skinny, wantTMDBCalls: 1, wantInserts: 1, wantKeywords: true},
		{name: "skinny row is served when TMDB fails", cached: skinny, tmdbErr: errors.New("tmdb down"), wantTMDBCalls: 1, wantInserts: 0, wantKeywords: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeTMDB{resp: fullDetail(550), err: tt.tmdbErr}
			repo := &fakeMovieRepo{cached: tt.cached}
			svc := movies.NewService(client, repo)

			got, err := svc.GetMovieById(context.Background(), 550, "tmdb")
			if err != nil {
				t.Fatalf("GetMovieById: %v", err)
			}
			if client.calls != tt.wantTMDBCalls {
				t.Errorf("TMDB calls = %d, want %d", client.calls, tt.wantTMDBCalls)
			}
			if len(repo.inserted) != tt.wantInserts {
				t.Errorf("inserts = %d, want %d", len(repo.inserted), tt.wantInserts)
			}
			if (got.Keywords != nil) != tt.wantKeywords {
				t.Errorf("keywords present = %v, want %v", got.Keywords != nil, tt.wantKeywords)
			}
		})
	}
}
