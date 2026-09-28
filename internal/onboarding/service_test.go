package onboarding_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/onboarding"
)

// fakePool serves canned pool rows and records the limit it was asked for.
type fakePool struct {
	rows      []onboarding.PoolRow
	gotLimits []int
}

func (f *fakePool) ListActiveWithCatalog(_ context.Context, limit int) ([]onboarding.PoolRow, error) {
	f.gotLimits = append(f.gotLimits, limit)
	return f.rows, nil
}

// fakeCatalog stands in for the cache-then-TMDB movies path and records
// which films it was asked to fetch.
type fakeCatalog struct {
	movies  map[int]*movies.Movie
	fetched []int
}

func (f *fakeCatalog) GetMovieById(_ context.Context, id int, _ string) (*movies.Movie, error) {
	f.fetched = append(f.fetched, id)
	if m, ok := f.movies[id]; ok {
		return m, nil
	}
	return nil, errors.New("tmdb unavailable")
}

func strPtr(s string) *string { return &s }

func date(year int) *time.Time {
	d := time.Date(year, 6, 1, 0, 0, 0, 0, time.UTC)
	return &d
}

func TestListEnriched_FetchesOnlyFilmsWithoutDetail(t *testing.T) {
	pool := &fakePool{rows: []onboarding.PoolRow{
		{TMDBID: 1, Dimension: "arthouse", Title: strPtr("Cached"), PosterPath: strPtr("/c.jpg"), ReleaseDate: date(2011), Detailed: true},
		{TMDBID: 2, Dimension: "auteur"},
		{TMDBID: 3, Dimension: "classic", Title: strPtr("From search"), PosterPath: strPtr("/s.jpg"), ReleaseDate: date(1974)},
		{TMDBID: 4, Dimension: "popcorn"},
	}}
	catalog := &fakeCatalog{movies: map[int]*movies.Movie{
		2: {Title: strPtr("Fetched"), PosterPath: strPtr("/f.jpg"), ReleaseDate: date(1994)},
		3: {Title: strPtr("From search"), PosterPath: strPtr("/s.jpg"), ReleaseDate: date(1974)},
	}}

	got, err := onboarding.NewService(pool, catalog).ListEnriched(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListEnriched: %v", err)
	}

	// Film 3 is cached but only from a search hit, so it goes through the
	// detail fetch too: the rating it gets must carry keywords.
	if want := []int{2, 3, 4}; !slices.Equal(catalog.fetched, want) {
		t.Errorf("fetched %v from the catalog, want %v", catalog.fetched, want)
	}
	// Film 4 failed to fetch and is left out, the rest keep pool order.
	want := []onboarding.EnrichedPoolEntry{
		{TMDBID: 1, Title: "Cached", PosterPath: "/c.jpg", ReleaseYear: 2011, Dimension: "arthouse"},
		{TMDBID: 2, Title: "Fetched", PosterPath: "/f.jpg", ReleaseYear: 1994, Dimension: "auteur"},
		{TMDBID: 3, Title: "From search", PosterPath: "/s.jpg", ReleaseYear: 1974, Dimension: "classic"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("entries =\n%+v\nwant\n%+v", got, want)
	}
}

func TestListEnriched_CapsTheLimit(t *testing.T) {
	tests := []struct {
		asked, want int
	}{
		{asked: 10, want: 10},
		{asked: onboarding.MaxPoolLimit, want: onboarding.MaxPoolLimit},
		{asked: 500, want: onboarding.MaxPoolLimit},
		{asked: 0, want: onboarding.MaxPoolLimit}, // 0 used to mean the whole pool
	}
	for _, tt := range tests {
		pool := &fakePool{}
		if _, err := onboarding.NewService(pool, &fakeCatalog{}).ListEnriched(context.Background(), tt.asked); err != nil {
			t.Fatalf("ListEnriched(%d): %v", tt.asked, err)
		}
		if !slices.Equal(pool.gotLimits, []int{tt.want}) {
			t.Errorf("ListEnriched(%d) read the pool with limits %v, want [%d]", tt.asked, pool.gotLimits, tt.want)
		}
	}
}
