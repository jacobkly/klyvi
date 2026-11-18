package movies_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"klyvi-api/internal/movies"
)

// fakeTMDB answers every request with one canned response, or an error.
type fakeTMDB struct {
	resp      map[string]any
	err       error
	calls     int
	endpoints []string
}

func (f *fakeTMDB) TMDBRequest(_ context.Context, _, endpoint string, _ any) (map[string]any, error) {
	f.calls++
	f.endpoints = append(f.endpoints, endpoint)
	return f.resp, f.err
}

// fakeMovieRepo keeps one cached row and records what the service writes.
type fakeMovieRepo struct {
	cached             *movies.Movie
	inserted           []*movies.Movie
	replaced           []*movies.Movie
	deleted            []int
	replacedCollection []movies.MovieCollection
	deletedCollections []int
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
func (r *fakeMovieRepo) ReplaceMovie(_ context.Context, m *movies.Movie) error {
	r.replaced = append(r.replaced, m)
	return nil
}
func (r *fakeMovieRepo) DeleteMovie(_ context.Context, id int) error {
	r.deleted = append(r.deleted, id)
	return nil
}
func (r *fakeMovieRepo) ReplaceCollection(_ context.Context, _ int, parts []movies.MovieCollection) error {
	r.replacedCollection = parts
	return nil
}
func (r *fakeMovieRepo) DeleteCollection(_ context.Context, id int) error {
	r.deletedCollections = append(r.deletedCollections, id)
	return nil
}

func rawJSON(s string) *json.RawMessage {
	rm := json.RawMessage(s)
	return &rm
}

func strPtr(s string) *string { return &s }

// fullDetail is a TMDB /movie/{id} response with keywords, credits, release
// dates and videos appended.
func fullDetail(id int) map[string]any {
	return map[string]any{
		"id":       float64(id),
		"title":    "Fight Club",
		"imdb_id":  "tt0137523",
		"genres":   []any{map[string]any{"id": float64(18), "name": "Drama"}},
		"keywords": map[string]any{"keywords": []any{map[string]any{"id": float64(825), "name": "support group"}}},
		"credits":  map[string]any{"cast": []any{}, "crew": []any{}},
		"release_dates": map[string]any{"results": []any{
			map[string]any{"iso_3166_1": "US", "release_dates": []any{
				map[string]any{"certification": "R", "type": float64(3)},
			}},
		}},
		"videos": map[string]any{"results": []any{
			map[string]any{"site": "YouTube", "type": "Trailer", "official": true, "key": "BdJKm16Co6M"},
		}},
	}
}

// A detail fetch asks TMDB for the extras in the same request and stores the
// rating, the trailer and the IMDb id.
func TestGetMovieById_StoresTheExtras(t *testing.T) {
	client := &fakeTMDB{resp: fullDetail(550)}
	repo := &fakeMovieRepo{}
	svc := movies.NewService(client, repo)

	if _, err := svc.GetMovieById(context.Background(), 550, "tmdb"); err != nil {
		t.Fatalf("GetMovieById: %v", err)
	}
	if len(client.endpoints) != 1 || !strings.Contains(client.endpoints[0], "release_dates,videos") {
		t.Errorf("endpoints = %v, want one detail request with the extras appended", client.endpoints)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserts = %d, want 1", len(repo.inserted))
	}
	m := repo.inserted[0]
	if m.Certification == nil || *m.Certification != "R" {
		t.Errorf("certification = %v, want R", m.Certification)
	}
	if m.TrailerKey == nil || *m.TrailerKey != "BdJKm16Co6M" {
		t.Errorf("trailer key = %v, want BdJKm16Co6M", m.TrailerKey)
	}
	if m.IMDBID == nil || *m.IMDBID != "tt0137523" {
		t.Errorf("imdb id = %v, want tt0137523", m.IMDBID)
	}
	if !m.HasExtras {
		t.Error("has_extras = false, want true")
	}
}

func TestGetMovieById_CachedRows(t *testing.T) {
	// A search hit is cached without keywords or credits, because TMDB's
	// search payload carries neither.
	skinny := &movies.Movie{MovieID: 550, Title: strPtr("Fight Club")}
	rich := &movies.Movie{
		MovieID:   550,
		Title:     strPtr("Fight Club"),
		Keywords:  rawJSON(`[{"id":825,"name":"support group"}]`),
		Credits:   rawJSON(`{"cast":[],"crew":[]}`),
		HasExtras: true,
	}
	// A full row cached before the extras were fetched.
	noExtras := *rich
	noExtras.HasExtras = false

	tests := []struct {
		name          string
		cached        *movies.Movie
		tmdbErr       error
		wantTMDBCalls int
		wantInserts   int
		wantReplaces  int
		wantKeywords  bool
	}{
		{name: "rich row is served from the cache", cached: rich, wantTMDBCalls: 0, wantInserts: 0, wantKeywords: true},
		{name: "skinny row is upgraded from TMDB", cached: skinny, wantTMDBCalls: 1, wantInserts: 1, wantKeywords: true},
		{name: "skinny row is served when TMDB fails", cached: skinny, tmdbErr: errors.New("tmdb down"), wantTMDBCalls: 1, wantInserts: 0, wantKeywords: false},
		{name: "row without extras is refetched and replaced", cached: &noExtras, wantTMDBCalls: 1, wantReplaces: 1, wantKeywords: true},
		{name: "row without extras is served when TMDB fails", cached: &noExtras, tmdbErr: errors.New("tmdb down"), wantTMDBCalls: 1, wantKeywords: true},
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
			if len(repo.replaced) != tt.wantReplaces {
				t.Errorf("replaces = %d, want %d", len(repo.replaced), tt.wantReplaces)
			}
			if (got.Keywords != nil) != tt.wantKeywords {
				t.Errorf("keywords present = %v, want %v", got.Keywords != nil, tt.wantKeywords)
			}
		})
	}
}
