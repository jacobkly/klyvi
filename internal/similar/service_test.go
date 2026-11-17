package similar_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/similar"
)

// fakeCatalog serves a canned source and pool, and counts how often the pool
// is read so a test can tell a cached shelf from a recomputed one.
type fakeCatalog struct {
	source   *similar.Source
	pool     []similar.Candidate
	resolved map[int]similar.Resolved

	// cachedOnEnsure is the source the catalog holds once ensure has run,
	// for a title that was not cached yet.
	cachedOnEnsure *similar.Source

	// byID are the titles ByTMDBIDs can return, and byIDReads how often it
	// was asked.
	byID      []similar.Candidate
	byIDReads int

	poolReads int
	minVotes  int
}

func (c *fakeCatalog) Source(_ context.Context, tmdbID int) (*similar.Source, error) {
	if c.source == nil || c.source.TMDBID != tmdbID {
		return nil, nil
	}
	return c.source, nil
}

func (c *fakeCatalog) Candidates(_ context.Context, _ *similar.Source, minVotes, limit int) ([]similar.Candidate, error) {
	c.poolReads++
	c.minVotes = minVotes
	if len(c.pool) > limit {
		return c.pool[:limit], nil
	}
	return c.pool, nil
}

func (c *fakeCatalog) ByTMDBIDs(_ context.Context, tmdbIDs []int, _ int) ([]similar.Candidate, error) {
	c.byIDReads++
	var out []similar.Candidate
	for _, cand := range c.byID {
		for _, id := range tmdbIDs {
			if cand.TMDBID == id {
				out = append(out, cand)
			}
		}
	}
	return out, nil
}

func (c *fakeCatalog) Resolve(_ context.Context, tmdbIDs []int) (map[int]similar.Resolved, error) {
	out := map[int]similar.Resolved{}
	for _, id := range tmdbIDs {
		if r, ok := c.resolved[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (c *fakeCatalog) ensure(_ context.Context, _ int) error {
	if c.cachedOnEnsure != nil {
		c.source = c.cachedOnEnsure
	}
	return nil
}

// fakeTMDB answers the recommendations endpoint with canned results.
type fakeTMDB struct {
	results []map[string]any
	err     error
	calls   []string
}

func (f *fakeTMDB) TMDBRequest(_ context.Context, _, endpoint string, _ interface{}) (map[string]interface{}, error) {
	f.calls = append(f.calls, endpoint)
	if f.err != nil {
		return nil, f.err
	}
	results := make([]any, len(f.results))
	for i, r := range f.results {
		results[i] = r
	}
	return map[string]any{"results": results}, nil
}

func movieResult(id int, title string) map[string]any {
	return map[string]any{
		"id": float64(id), "title": title, "release_date": "2019-05-30", "poster_path": "/p.jpg",
	}
}

var testConfig = similar.Config{KeywordWeight: 0.9, GenreWeight: 0.2, MinVotes: 25}

func newMovieService(c *fakeCatalog, t *fakeTMDB, idf *reco.FeatureIDF) *similar.Service {
	return similar.NewMovieService(c, c.ensure, t, idf, testConfig)
}

// candidate is a pool title sharing the given keywords and genres with the
// source.
func candidate(id int, keywords []int, genres []int) similar.Candidate {
	return similar.Candidate{
		TMDBID: id, MediaID: id + 1000, Title: fmt.Sprintf("title %d", id),
		Year: 2020, GenreIDs: genres, KeywordIDs: keywords,
	}
}

func tmdbIDs(items []similar.Item) []int {
	ids := make([]int, len(items))
	for i, it := range items {
		ids[i] = it.TMDBID
	}
	return ids
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// keywordRichSource carries enough keywords that its overlap leads the shelf.
func keywordRichSource() *similar.Source {
	return &similar.Source{
		TMDBID:       1,
		GenreIDs:     []int{18},
		KeywordIDs:   []int{101, 102, 103, 104},
		GenreNames:   map[int]string{18: "Drama"},
		KeywordNames: map[int]string{101: "class differences", 102: "based on novel", 103: "social satire", 104: "con artist"},
	}
}

// A keyword most of the catalog carries says little about what a title is
// like. Two rare shared keywords beat one common one, and the reasons name
// the rare ones first.
func TestSimilar_RareSharedKeywordsRankFirst(t *testing.T) {
	src := keywordRichSource()
	// 102 is on half the catalog, the rest are on a handful of titles.
	idf := reco.NewFeatureIDF(map[int]int{18: 400}, map[int]int{101: 3, 102: 500, 103: 4, 104: 5}, 1000)
	pool := []similar.Candidate{
		candidate(20, []int{102, 900}, []int{18}),      // one common keyword
		candidate(21, []int{101, 103, 901}, []int{18}), // two rare ones
		candidate(22, []int{104, 902, 903}, []int{35}), // one rare one, other genre
	}
	for i := 23; i < 23+similar.MaxLimit; i++ {
		pool = append(pool, candidate(i, []int{102, 999, 998, 997, 996}, nil))
	}
	c := &fakeCatalog{source: src, pool: pool}

	got, err := newMovieService(c, &fakeTMDB{}, idf).Similar(context.Background(), 1, 3)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if want := []int{21, 22, 20}; !sameInts(tmdbIDs(got), want) {
		t.Fatalf("order = %v, want %v", tmdbIDs(got), want)
	}
	top := got[0]
	if len(top.Reasons) < 2 || top.Reasons[0].Name != "class differences" || top.Reasons[1].Name != "social satire" {
		t.Errorf("reasons = %+v, want the two rare keywords first", top.Reasons)
	}
	if top.Reasons[0].Kind != "keyword" || top.Reasons[0].ID != 101 {
		t.Errorf("first reason = %+v, want keyword 101", top.Reasons[0])
	}
	if top.MediaID == nil || *top.MediaID != 1021 || top.MediaType != "movie" {
		t.Errorf("top item = %+v, want media_id 1021 and type movie", top)
	}
	if c.minVotes != testConfig.MinVotes {
		t.Errorf("pool read with min votes %d, want the floor %d", c.minVotes, testConfig.MinVotes)
	}
}

func TestSimilar_ScoreIsASimilarityBetweenZeroAndOne(t *testing.T) {
	src := keywordRichSource()
	twin := candidate(20, src.KeywordIDs, src.GenreIDs)
	partial := candidate(21, []int{101, 900, 901, 902}, []int{35})
	c := &fakeCatalog{source: src, pool: []similar.Candidate{twin, partial}}

	got, err := newMovieService(c, &fakeTMDB{}, nil).Similar(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].Score != 1 {
		t.Errorf("a title with the same keywords and genres scored %v, want 1", got[0].Score)
	}
	if got[1].Score <= 0 || got[1].Score >= 1 {
		t.Errorf("a partial match scored %v, want between 0 and 1", got[1].Score)
	}
}

// Sequels share most of their keywords, so without a cap a franchise would
// fill the shelf. The source never appears on its own shelf.
func TestSimilar_NeverTheSourceAndAtMostTwoPerCollection(t *testing.T) {
	src := keywordRichSource()
	src.CollectionID = 77
	var pool []similar.Candidate
	pool = append(pool, candidate(1, src.KeywordIDs, src.GenreIDs)) // the source itself
	for id := 30; id < 34; id++ {
		sequel := candidate(id, src.KeywordIDs, src.GenreIDs)
		sequel.CollectionID = 77
		pool = append(pool, sequel)
	}
	for id := 40; id < 40+similar.MaxLimit; id++ {
		pool = append(pool, candidate(id, []int{101, 900}, nil))
	}
	c := &fakeCatalog{source: src, pool: pool}

	got, err := newMovieService(c, &fakeTMDB{}, nil).Similar(context.Background(), 1, similar.MaxLimit)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if len(got) != similar.MaxLimit {
		t.Fatalf("got %d items, want %d", len(got), similar.MaxLimit)
	}
	inCollection := 0
	for _, it := range got {
		if it.TMDBID == 1 {
			t.Error("the source is on its own shelf")
		}
		if it.TMDBID >= 30 && it.TMDBID < 34 {
			inCollection++
		}
	}
	if inCollection != 2 {
		t.Errorf("%d titles from the source's collection, want 2", inCollection)
	}
	if got[0].TMDBID != 30 || got[1].TMDBID != 31 {
		t.Errorf("shelf starts %v, want the two best-matching sequels first", tmdbIDs(got)[:2])
	}
}

// When the overlap is thin, TMDB's own list fills the rest of the shelf. A
// fill title the catalog has carries its media_id and counts toward the
// collection cap. One it lacks still shows, with a null media_id.
func TestSimilar_FillsFromTMDBWhenTheOverlapIsThin(t *testing.T) {
	src := keywordRichSource()
	c := &fakeCatalog{
		source: src,
		pool: []similar.Candidate{
			candidate(20, []int{101, 103}, []int{18}),
			func() similar.Candidate { s := candidate(21, []int{101}, nil); s.CollectionID = 9; return s }(),
		},
		resolved: map[int]similar.Resolved{
			50: {MediaID: 5050},
			51: {MediaID: 5051, CollectionID: 9},
			52: {MediaID: 5052, CollectionID: 9}, // a third from collection 9
		},
	}
	tm := &fakeTMDB{results: []map[string]any{
		movieResult(20, "already on the shelf"),
		movieResult(1, "the source"),
		movieResult(50, "cached"),
		movieResult(51, "same collection as 21"),
		movieResult(52, "third of collection 9"),
		movieResult(53, "not cached"),
		{"id": float64(54), "title": "adult", "adult": true},
	}}

	got, err := newMovieService(c, tm, nil).Similar(context.Background(), 1, 12)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if want := []int{20, 21, 50, 51, 53}; !sameInts(tmdbIDs(got), want) {
		t.Fatalf("shelf = %v, want %v", tmdbIDs(got), want)
	}
	if len(tm.calls) != 1 || tm.calls[0] != "/movie/1/recommendations?language=en-US&page=1" {
		t.Errorf("TMDB calls = %v, want the movie's recommendations page", tm.calls)
	}
	cached, uncached := got[2], got[4]
	if cached.MediaID == nil || *cached.MediaID != 5050 {
		t.Errorf("cached fill media_id = %v, want 5050", cached.MediaID)
	}
	if uncached.MediaID != nil {
		t.Errorf("uncached fill media_id = %v, want null", *uncached.MediaID)
	}
	if uncached.Title != "not cached" || uncached.Year != 2019 || uncached.PosterPath != "/p.jpg" {
		t.Errorf("fill item = %+v, want TMDB's title, year and poster", uncached)
	}
	if uncached.Score != 0 || uncached.Reasons == nil || len(uncached.Reasons) != 0 {
		t.Errorf("fill item score %v reasons %v, want 0 and an empty list", uncached.Score, uncached.Reasons)
	}
}

// A title with only a keyword or two matches on tags too broad to trust, so
// TMDB's list leads and the overlap follows.
func TestSimilar_AKeywordPoorTitleLeadsWithTMDB(t *testing.T) {
	src := &similar.Source{TMDBID: 1, KeywordIDs: []int{101}, KeywordNames: map[int]string{101: "christmas"}}
	c := &fakeCatalog{source: src, pool: []similar.Candidate{candidate(20, []int{101}, nil)}}
	tm := &fakeTMDB{results: []map[string]any{movieResult(50, "from TMDB")}}

	got, err := newMovieService(c, tm, nil).Similar(context.Background(), 1, 12)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if want := []int{50, 20}; !sameInts(tmdbIDs(got), want) {
		t.Errorf("shelf = %v, want %v", tmdbIDs(got), want)
	}
}

// A title nobody has opened yet is cached first, so its shelf comes from
// the overlap like any other.
func TestSimilar_CachesAnUncachedTitleFirst(t *testing.T) {
	src := keywordRichSource()
	var pool []similar.Candidate
	for id := 20; id < 20+similar.MaxLimit; id++ {
		pool = append(pool, candidate(id, []int{101, 102}, nil))
	}
	c := &fakeCatalog{cachedOnEnsure: src, pool: pool}
	tm := &fakeTMDB{}

	got, err := newMovieService(c, tm, nil).Similar(context.Background(), 1, 12)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if len(got) != 12 || c.poolReads != 1 {
		t.Errorf("got %d items from %d pool reads, want 12 from 1", len(got), c.poolReads)
	}
	if len(tm.calls) != 0 {
		t.Errorf("TMDB calls = %v, want none for a full overlap", tm.calls)
	}
}

func TestSimilar_AnUnknownTitleIsNotFound(t *testing.T) {
	c := &fakeCatalog{}
	tm := &fakeTMDB{err: &tmdb.StatusError{Status: 404}}

	_, err := newMovieService(c, tm, nil).Similar(context.Background(), 999, 12)
	if !errors.Is(err, similar.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// With TMDB down the overlap still makes a shelf. It is not kept, so the
// next request tries to fill it again.
func TestSimilar_TMDBDownServesTheOverlapUncached(t *testing.T) {
	src := keywordRichSource()
	c := &fakeCatalog{source: src, pool: []similar.Candidate{candidate(20, []int{101}, nil)}}
	tm := &fakeTMDB{err: errors.New("connection reset")}
	svc := newMovieService(c, tm, nil)

	for range 2 {
		got, err := svc.Similar(context.Background(), 1, 12)
		if err != nil {
			t.Fatalf("Similar: %v", err)
		}
		if !sameInts(tmdbIDs(got), []int{20}) {
			t.Errorf("shelf = %v, want the overlap", tmdbIDs(got))
		}
	}
	if c.poolReads != 2 {
		t.Errorf("pool read %d times, want 2 (a partial shelf is not kept)", c.poolReads)
	}
}

func TestSimilar_TMDBDownWithNoOverlapIsAnError(t *testing.T) {
	c := &fakeCatalog{}
	tm := &fakeTMDB{err: errors.New("connection reset")}

	_, err := newMovieService(c, tm, nil).Similar(context.Background(), 1, 12)
	if err == nil || errors.Is(err, similar.ErrNotFound) {
		t.Errorf("err = %v, want an upstream error", err)
	}
}

// A shelf is computed once for every limit: later requests slice the kept
// one.
func TestSimilar_KeepsACompleteShelf(t *testing.T) {
	src := keywordRichSource()
	var pool []similar.Candidate
	for id := 20; id < 20+similar.MaxLimit; id++ {
		pool = append(pool, candidate(id, []int{101}, nil))
	}
	c := &fakeCatalog{source: src, pool: pool}
	svc := newMovieService(c, &fakeTMDB{}, nil)

	first, err := svc.Similar(context.Background(), 1, 12)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	more, err := svc.Similar(context.Background(), 1, similar.MaxLimit)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if len(first) != 12 || len(more) != similar.MaxLimit || c.poolReads != 1 {
		t.Errorf("got %d then %d items from %d pool reads, want 12, %d and 1",
			len(first), len(more), c.poolReads, similar.MaxLimit)
	}
}

func TestSimilar_TVAsksTMDBForTheSeriesList(t *testing.T) {
	c := &fakeCatalog{}
	tm := &fakeTMDB{results: []map[string]any{
		{"id": float64(60), "name": "A Show", "first_air_date": "2008-01-20", "poster_path": "/s.jpg"},
	}}
	svc := similar.NewTVService(c, c.ensure, tm, nil, testConfig)

	got, err := svc.Similar(context.Background(), 1396, 12)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if len(tm.calls) != 1 || !strings.HasPrefix(tm.calls[0], "/tv/1396/recommendations") {
		t.Errorf("TMDB calls = %v, want the series' recommendations", tm.calls)
	}
	if len(got) != 1 || got[0].Title != "A Show" || got[0].Year != 2008 || got[0].MediaType != "tv" {
		t.Errorf("got %+v, want the series named from TMDB's name field", got)
	}
}

func plotVec(v ...float32) []float32 { return v }

// plotShelf is a keyword-rich source, 1, whose story is nearly that of title
// 3, which shares none of its keywords. Title 2 shares a keyword and has an
// unrelated story, and title 4 shares a keyword and has no plot vector.
func plotShelf() (*fakeCatalog, *reco.PlotIndex) {
	c := &fakeCatalog{
		source: &similar.Source{TMDBID: 1, KeywordIDs: []int{10, 11, 12}, GenreIDs: []int{18}},
		pool: []similar.Candidate{
			candidate(2, []int{10}, []int{18}),
			candidate(4, []int{10}, []int{18}),
		},
		byID: []similar.Candidate{candidate(3, []int{99}, []int{18})},
	}
	idx := reco.NewPlotIndex([]reco.TitlePlot{
		{MediaType: "movie", TMDBID: 1, Vector: plotVec(1, 0, 0)},
		{MediaType: "movie", TMDBID: 3, Vector: plotVec(0.998, 0.05, 0)},
		{MediaType: "movie", TMDBID: 2, Vector: plotVec(0, 1, 0)},
		{MediaType: "movie", TMDBID: 5, Vector: plotVec(0, 0, 1)},
		{MediaType: "movie", TMDBID: 6, Vector: plotVec(-0.577, -0.577, -0.577)},
	})
	return c, idx
}

func TestSimilar_PlotNeighboursJoinTheShelf(t *testing.T) {
	ctx := context.Background()

	offCat, idx := plotShelf()
	off, err := newMovieService(offCat, &fakeTMDB{}, nil).WithPlots(idx, 0).Similar(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !sameInts(tmdbIDs(off), []int{2, 4}) || offCat.byIDReads != 0 {
		t.Errorf("weight 0: shelf %v after %d plot reads, want [2 4] and none", tmdbIDs(off), offCat.byIDReads)
	}

	onCat, idx := plotShelf()
	// At weight 2 the story counts for more than the keywords (0.9), so a
	// near-identical plot outranks a title sharing one keyword of three.
	on, err := newMovieService(onCat, &fakeTMDB{}, nil).WithPlots(idx, 2).Similar(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if ids := tmdbIDs(on); len(ids) != 3 || ids[0] != 3 {
		t.Fatalf("plot on: shelf %v, want the same-story title 3 first", ids)
	}
	score := func(items []similar.Item, id int) float64 {
		for _, it := range items {
			if it.TMDBID == id {
				return it.Score
			}
		}
		return -1
	}
	if score(on, 4) != score(off, 4) {
		t.Errorf("a title with no plot vector moved: %v off, %v on", score(off, 4), score(on, 4))
	}
	if on[0].Score <= 0 || on[0].Score > 1 {
		t.Errorf("a plot neighbour's score %v is outside (0, 1]", on[0].Score)
	}
}

func TestSimilar_PlotsWithoutASourceVectorChangeNothing(t *testing.T) {
	c, idx := plotShelf()
	c.source.TMDBID = 7 // no plot vector
	got, err := newMovieService(c, &fakeTMDB{}, nil).WithPlots(idx, 1).Similar(context.Background(), 7, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !sameInts(tmdbIDs(got), []int{2, 4}) || c.byIDReads != 0 {
		t.Errorf("shelf %v after %d plot reads, want today's [2 4]", tmdbIDs(got), c.byIDReads)
	}
}
