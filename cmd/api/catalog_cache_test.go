package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"klyvi-api/internal/reco"
)

func film(id, votes int, genres, keywords []int) cacheEntry {
	return cacheEntry{
		cand:     reco.Candidate{MediaID: id, MediaType: "movie", Features: &reco.MediaFeatures{MediaID: id, MediaType: "movie", VoteCount: votes}},
		genres:   genres,
		keywords: keywords,
		votes:    votes,
	}
}

func show(id, votes int, genres, keywords []int) cacheEntry {
	e := film(id, votes, genres, keywords)
	e.cand.MediaType, e.cand.Features.MediaType = "tv", "tv"
	return e
}

func ids(cands []reco.Candidate) []int {
	out := make([]int, len(cands))
	for i, c := range cands {
		out[i] = c.MediaID
	}
	return out
}

// fakeSQL stands in for the database adapter the cache falls back to, and
// records what reached it.
type fakeSQL struct {
	reco.CatalogRepository
	askedIDs   [][]int
	tasteCalls int
	byID       map[int]reco.Candidate
}

func (f *fakeSQL) CandidatesByMediaIDs(_ context.Context, ids []int) ([]reco.Candidate, error) {
	f.askedIDs = append(f.askedIDs, ids)
	var out []reco.Candidate
	for _, id := range ids {
		if c, ok := f.byID[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeSQL) CandidatesByTaste(context.Context, []int, []int, int, int) ([]reco.Candidate, error) {
	f.tasteCalls++
	return nil, nil
}

// loaderOf serves rows and records each since it was asked for.
type fakeLoader struct {
	rows  []cacheEntry
	since []time.Time
}

func (l *fakeLoader) load(_ context.Context, since time.Time) ([]cacheEntry, error) {
	l.since = append(l.since, since)
	return l.rows, nil
}

func loadedCache(t *testing.T, rows ...cacheEntry) (*cachedCatalog, *fakeSQL) {
	t.Helper()
	sql := &fakeSQL{}
	l := &fakeLoader{rows: rows}
	c := newCachedCatalog(sql, l.load)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, sql
}

// Keyword matches come first, most voted first, then genre-only matches fill
// the rest, as the SQL draw does. Ties go to the lower media id, and titles
// under the vote floor never enter.
func TestCachedCatalog_CandidatesByTaste(t *testing.T) {
	c, _ := loadedCache(t,
		film(1, 500, []int{18}, nil),
		film(2, 90, []int{35}, []int{7}),
		film(3, 300, []int{18}, []int{7}),
		film(4, 300, []int{18}, nil),
		film(5, 10, []int{18}, []int{7}),
		film(6, 40, []int{99}, nil),
		show(7, 900, []int{18}, []int{7}),
	)
	got, err := c.CandidatesByTaste(context.Background(), []int{18}, []int{7}, 25, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{3, 2, 1, 4}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("CandidatesByTaste = %v, want %v", ids(got), want)
	}

	got, _ = c.CandidatesByTaste(context.Background(), []int{18}, []int{7}, 25, 3)
	if want := []int{3, 2, 1}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("limit 3 = %v, want %v", ids(got), want)
	}
}

// The series draw reads only series, and matches genres in TV's own id space,
// as stored.
func TestCachedCatalog_SeriesCandidatesByTaste(t *testing.T) {
	c, _ := loadedCache(t,
		show(10, 50, []int{10765}, nil),
		show(11, 80, []int{18}, []int{3}),
		film(12, 999, []int{10765}, []int{3}),
	)
	got, _ := c.SeriesCandidatesByTaste(context.Background(), []int{10765}, []int{3}, 25, 10)
	if want := []int{11, 10}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("SeriesCandidatesByTaste = %v, want %v", ids(got), want)
	}
}

// The popular draw is the most-voted titles of one type, unvoted ones left out.
func TestCachedCatalog_Samples(t *testing.T) {
	c, _ := loadedCache(t,
		film(1, 5, nil, nil),
		film(2, 0, nil, nil),
		film(3, 50, nil, nil),
		show(4, 7, nil, nil),
	)
	films, _ := c.SampleMovieCandidates(context.Background(), 5)
	if want := []int{3, 1}; !reflect.DeepEqual(ids(films), want) {
		t.Errorf("films = %v, want %v", ids(films), want)
	}
	series, _ := c.SampleSeriesCandidates(context.Background(), 5)
	if want := []int{4}; !reflect.DeepEqual(ids(series), want) {
		t.Errorf("series = %v, want %v", ids(series), want)
	}
}

// A history's titles come from memory, unvoted ones included. Only ids the
// cache does not hold reach the database.
func TestCachedCatalog_CandidatesByMediaIDs(t *testing.T) {
	c, sql := loadedCache(t, film(1, 0, nil, nil), show(2, 9, nil, nil))
	sql.byID = map[int]reco.Candidate{3: {MediaID: 3, MediaType: "movie"}}

	got, err := c.CandidatesByMediaIDs(context.Background(), []int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("CandidatesByMediaIDs = %v, want %v", ids(got), want)
	}
	if want := [][]int{{3}}; !reflect.DeepEqual(sql.askedIDs, want) {
		t.Errorf("database asked for %v, want %v", sql.askedIDs, want)
	}

	sql.askedIDs = nil
	if _, err := c.CandidatesByMediaIDs(context.Background(), []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if sql.askedIDs != nil {
		t.Errorf("a history the cache holds should make no query, asked %v", sql.askedIDs)
	}
}

// Until the first load lands, the feed reads the database as before.
func TestCachedCatalog_FallsBackUntilLoaded(t *testing.T) {
	sql := &fakeSQL{}
	c := newCachedCatalog(sql, (&fakeLoader{}).load)
	if _, err := c.CandidatesByTaste(context.Background(), []int{18}, nil, 0, 5); err != nil {
		t.Fatal(err)
	}
	if sql.tasteCalls != 1 {
		t.Errorf("taste draws on the database = %d, want 1", sql.tasteCalls)
	}
}

// A refresh reads only rows changed since the last one (with a lookback for
// late commits), so a title added by search joins, and a changed vote count
// moves it. Every so often a full reload drops titles that left the catalog.
func TestCachedCatalog_Refresh(t *testing.T) {
	start := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	clock := start
	l := &fakeLoader{rows: []cacheEntry{film(1, 100, nil, nil), film(2, 50, nil, nil)}}
	c := newCachedCatalog(&fakeSQL{}, l.load)
	c.now = func() time.Time { return clock }

	ctx := context.Background()
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !l.since[0].IsZero() {
		t.Errorf("first load since %v, want the zero time", l.since[0])
	}

	clock = start.Add(2 * time.Minute)
	l.rows = []cacheEntry{film(2, 500, nil, nil), film(3, 70, nil, nil)}
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if want := start.Add(-catalogLookback); !l.since[1].Equal(want) {
		t.Errorf("refresh since %v, want %v", l.since[1], want)
	}
	got, _ := c.SampleMovieCandidates(ctx, 10)
	if want := []int{2, 1, 3}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("after refresh = %v, want %v", ids(got), want)
	}

	clock = start.Add(catalogFullReload + time.Minute)
	l.rows = []cacheEntry{film(3, 70, nil, nil)}
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !l.since[2].IsZero() {
		t.Errorf("full reload since %v, want the zero time", l.since[2])
	}
	got, _ = c.SampleMovieCandidates(ctx, 10)
	if want := []int{3}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("after full reload = %v, want %v", ids(got), want)
	}
}
