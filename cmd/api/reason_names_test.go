package main

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// countingNames answers from a fixed table and records every id it was asked
// for, so tests can see which lookups reached the database.
type countingNames struct {
	genres, keywords         map[int]string
	askedGenres, askedKwords [][]int
}

func (c *countingNames) LookupGenreNames(_ context.Context, ids []int) (map[int]string, error) {
	c.askedGenres = append(c.askedGenres, sorted(ids))
	return pick(c.genres, ids), nil
}

func (c *countingNames) LookupKeywordNames(_ context.Context, ids []int) (map[int]string, error) {
	c.askedKwords = append(c.askedKwords, sorted(ids))
	return pick(c.keywords, ids), nil
}

func pick(table map[int]string, ids []int) map[int]string {
	out := map[int]string{}
	for _, id := range ids {
		if name, ok := table[id]; ok {
			out[id] = name
		}
	}
	return out
}

func sorted(ids []int) []int {
	out := append([]int(nil), ids...)
	sort.Ints(out)
	return out
}

func TestReasonNames_QueriesEachIDOnce(t *testing.T) {
	db := &countingNames{
		genres:   map[int]string{18: "Drama", 53: "Thriller"},
		keywords: map[int]string{825: "support group", 9672: "based on true story"},
	}
	names := newReasonNames(db)
	ctx := context.Background()

	g, k, err := names.Lookup(ctx, []int{18}, []int{825, 404})
	if err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	if !reflect.DeepEqual(g, map[int]string{18: "Drama"}) || !reflect.DeepEqual(k, map[int]string{825: "support group"}) {
		t.Fatalf("first Lookup = %v %v", g, k)
	}

	// Everything is known now, including that 404 has no name.
	g, k, err = names.Lookup(ctx, []int{18}, []int{404, 825})
	if err != nil {
		t.Fatalf("repeat Lookup: %v", err)
	}
	if !reflect.DeepEqual(g, map[int]string{18: "Drama"}) || !reflect.DeepEqual(k, map[int]string{825: "support group"}) {
		t.Fatalf("repeat Lookup = %v %v", g, k)
	}

	// Only the new ids reach the database.
	if _, k, err = names.Lookup(ctx, []int{18, 53}, []int{825, 9672}); err != nil {
		t.Fatalf("partial Lookup: %v", err)
	}
	if k[9672] != "based on true story" {
		t.Errorf("partial Lookup keywords = %v", k)
	}

	wantGenres := [][]int{{18}, {53}}
	wantKwords := [][]int{{404, 825}, {9672}}
	if !reflect.DeepEqual(db.askedGenres, wantGenres) || !reflect.DeepEqual(db.askedKwords, wantKwords) {
		t.Errorf("DB asked genres %v keywords %v, want %v and %v", db.askedGenres, db.askedKwords, wantGenres, wantKwords)
	}
}

// Series carry genres no film does (Kids, Reality, Talk) and keywords no
// cached film names. Those resolve from the series cache, which is only asked
// about the ids the film cache could not name.
func TestReasonNames_FallsBackToTheSeriesCache(t *testing.T) {
	films := &countingNames{
		genres:   map[int]string{878: "Science Fiction"},
		keywords: map[int]string{825: "support group"},
	}
	series := &countingNames{
		genres:   map[int]string{10762: "Kids"},
		keywords: map[int]string{7000: "anthology"},
	}
	names := newReasonNames(films, series)
	ctx := context.Background()

	g, k, err := names.Lookup(ctx, []int{878, 10762}, []int{825, 7000, 404})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !reflect.DeepEqual(g, map[int]string{878: "Science Fiction", 10762: "Kids"}) {
		t.Errorf("genres = %v", g)
	}
	if !reflect.DeepEqual(k, map[int]string{825: "support group", 7000: "anthology"}) {
		t.Errorf("keywords = %v", k)
	}
	if !reflect.DeepEqual(series.askedGenres, [][]int{{10762}}) || !reflect.DeepEqual(series.askedKwords, [][]int{{404, 7000}}) {
		t.Errorf("series cache asked genres %v keywords %v, want only the ids films lack", series.askedGenres, series.askedKwords)
	}

	// Ids neither cache names are remembered too.
	if _, _, err := names.Lookup(ctx, []int{10762}, []int{404}); err != nil {
		t.Fatalf("repeat Lookup: %v", err)
	}
	if len(films.askedKwords) != 1 || len(series.askedKwords) != 1 {
		t.Errorf("repeat Lookup reached the database: films %v, series %v", films.askedKwords, series.askedKwords)
	}
}
