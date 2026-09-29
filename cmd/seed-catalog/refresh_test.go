package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"slices"
	"testing"
	"time"

	"klyvi-api/internal/platform/tmdb"
)

var day0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeCatalog is a set of stale sources whose items record the order they are
// refreshed in and answer with a canned outcome.
type fakeCatalog struct {
	order []string
}

type fakeItem struct {
	label   string
	ageDays int
	removed bool
	err     error
	then    func() // runs after the refresh, e.g. to cancel the run
}

func (c *fakeCatalog) source(name string, items ...fakeItem) staleSource {
	return staleSource{
		name: name,
		list: func(_ context.Context, cutoff time.Time, limit int) ([]staleItem, error) {
			var out []staleItem
			for _, it := range items {
				updated := day0.AddDate(0, 0, it.ageDays)
				if !updated.Before(cutoff) || len(out) == limit {
					continue
				}
				out = append(out, staleItem{
					label:     it.label,
					updatedAt: updated,
					refresh: func(context.Context) (bool, error) {
						c.order = append(c.order, it.label)
						if it.then != nil {
							it.then()
						}
						return it.removed, it.err
					},
				})
			}
			return out, nil
		},
	}
}

func testRefresher(sources ...staleSource) refresher {
	return refresher{
		sources:          sources,
		cutoff:           day0.AddDate(0, 0, 100),
		throttlePause:    time.Millisecond,
		maxRemovedInARow: 20,
		maxFailedInARow:  20,
	}
}

func TestRefresh_OldestFirstAcrossTablesUpToTheCap(t *testing.T) {
	c := &fakeCatalog{}
	r := testRefresher(
		c.source("movies", fakeItem{label: "movie 3", ageDays: 3}, fakeItem{label: "movie 1", ageDays: 1}),
		c.source("series", fakeItem{label: "series 2", ageDays: 2}, fakeItem{label: "series fresh", ageDays: 200}),
		c.source("seasons", fakeItem{label: "season 0", ageDays: 0}),
	)
	r.max = 3

	got, err := r.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := []string{"season 0", "movie 1", "series 2"}; !slices.Equal(c.order, want) {
		t.Errorf("refreshed %v, want %v", c.order, want)
	}
	if got.refreshed != 3 || got.left != 1 {
		t.Errorf("refreshed %d, left %d; want 3 and 1", got.refreshed, got.left)
	}
}

func TestRefresh_CountsEachOutcome(t *testing.T) {
	c := &fakeCatalog{}
	r := testRefresher(c.source("movies",
		fakeItem{label: "kept", ageDays: 1},
		fakeItem{label: "gone", ageDays: 2, removed: true},
		fakeItem{label: "broken", ageDays: 3, err: errors.New("connection reset")},
		fakeItem{label: "kept too", ageDays: 4},
	))

	got, err := r.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.refreshed != 2 || got.removed != 1 || got.failed != 1 || got.left != 1 {
		t.Errorf("got %+v, want 2 refreshed, 1 removed, 1 failed, 1 left", got)
	}
	if len(c.order) != 4 {
		t.Errorf("refreshed %v, want all four tried", c.order)
	}
}

// A 429 means TMDB wants the run to slow down. It waits, then carries on.
func TestRefresh_PausesWhenTMDBThrottles(t *testing.T) {
	c := &fakeCatalog{}
	r := testRefresher(c.source("movies",
		fakeItem{label: "throttled", ageDays: 1, err: &tmdb.StatusError{Status: http.StatusTooManyRequests}},
		fakeItem{label: "next", ageDays: 2},
	))
	r.throttlePause = 50 * time.Millisecond

	start := time.Now()
	got, err := r.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if elapsed := time.Since(start); elapsed < r.throttlePause {
		t.Errorf("run took %v, want at least the %v pause", elapsed, r.throttlePause)
	}
	if got.failed != 1 || got.refreshed != 1 {
		t.Errorf("got %+v, want the throttled item failed and the next refreshed", got)
	}
}

// The time budget ends the run between items. What is left stays stale and
// the next run picks it up first.
func TestRefresh_StopsWhenTheTimeBudgetEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &fakeCatalog{}
	r := testRefresher(c.source("movies",
		fakeItem{label: "first", ageDays: 1, then: cancel},
		fakeItem{label: "second", ageDays: 2},
		fakeItem{label: "third", ageDays: 3},
	))

	got, err := r.run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !slices.Equal(c.order, []string{"first"}) {
		t.Errorf("refreshed %v, want only the first", c.order)
	}
	if !got.outOfTime || got.left != 2 {
		t.Errorf("got %+v, want out of time with 2 left", got)
	}
}

// A run of removals or failures means something is wrong with the requests,
// not with TMDB's catalog. The run stops before it empties the cache.
func TestRefresh_StopsOnARunOfRemovalsOrFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		item fakeItem
	}{
		{"removals", fakeItem{removed: true}},
		{"failures", fakeItem{err: errors.New("connection reset")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeCatalog{}
			items := make([]fakeItem, 10)
			for i := range items {
				items[i] = tt.item
				items[i].label = string(rune('a' + i))
				items[i].ageDays = i
			}
			r := testRefresher(c.source("movies", items...))
			r.maxRemovedInARow, r.maxFailedInARow = 3, 3

			if _, err := r.run(context.Background()); err == nil {
				t.Fatal("run succeeded, want it to stop with an error")
			}
			if len(c.order) != 3 {
				t.Errorf("tried %d items, want the run to stop at 3", len(c.order))
			}
		})
	}
}

func TestAgeFlag(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "150d", want: 150 * 24 * time.Hour},
		{in: "0d", want: 0},
		{in: "36h", want: 36 * time.Hour},
		{in: "181d", wantErr: true}, // past TMDB's six month limit
		{in: "-1d", wantErr: true},
		{in: "soon", wantErr: true},
	}
	for _, tt := range tests {
		fs := flag.NewFlagSet("seed", flag.ContinueOnError)
		fs.SetOutput(nopWriter{})
		var a ageFlag
		fs.Var(&a, "refresh-older-than", "")
		err := fs.Parse([]string{"-refresh-older-than", tt.in})
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && (!a.set || a.d != tt.want) {
			t.Errorf("%s: got %+v, want %v and set", tt.in, a, tt.want)
		}
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
