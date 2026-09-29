package main

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// fakeLists answers every list request with one result and one page. Popular
// and top rated return the same id, and each discover bucket returns a new one.
type fakeLists struct {
	calls []string
	next  int
}

func (f *fakeLists) TMDBRequest(_ context.Context, _, endpoint string, _ interface{}) (map[string]interface{}, error) {
	f.calls = append(f.calls, endpoint)
	id := 1396
	if strings.HasPrefix(endpoint, "/discover/") {
		f.next++
		id = 10_000 + f.next
	}
	return map[string]interface{}{
		"results":     []interface{}{map[string]interface{}{"id": float64(id)}},
		"total_pages": float64(1),
	}, nil
}

func TestCollectIDs_TVSweep(t *testing.T) {
	f := &fakeLists{}
	ids, err := collectIDs(context.Background(), f, tvSweep,
		sweepLimits{popularPages: 2, topRatedPages: 2, discoverPages: 3, minVotes: 20})
	if err != nil {
		t.Fatalf("collectIDs: %v", err)
	}

	buckets := len(tvSweep.genres) * len(tvSweep.windows)
	if len(ids) != 1+buckets {
		t.Errorf("collected %d ids, want %d (one shared list id plus one per bucket)", len(ids), 1+buckets)
	}
	if n := countOf(ids, 1396); n != 1 {
		t.Errorf("id 1396 collected %d times, want once", n)
	}

	for _, call := range f.calls {
		switch {
		case strings.HasPrefix(call, "/tv/popular?"), strings.HasPrefix(call, "/tv/top_rated?"):
		case strings.HasPrefix(call, "/discover/tv?"):
			for _, want := range []string{"first_air_date.gte=", "first_air_date.lte=", "vote_count.gte=20", "sort_by=vote_count.desc"} {
				if !strings.Contains(call, want) {
					t.Errorf("discover call %q lacks %q", call, want)
				}
			}
		default:
			t.Errorf("TV sweep called %q", call)
		}
	}
}

// TV collects far fewer votes than film, so without -min-votes each catalog
// uses its own floor.
func TestCollectIDs_DefaultVoteFloorPerCatalog(t *testing.T) {
	for _, tc := range []struct {
		name string
		sw   sweep
		want string
	}{
		{"movie", movieSweep, "vote_count.gte=50"},
		{"tv", tvSweep, "vote_count.gte=20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeLists{}
			if _, err := collectIDs(context.Background(), f, tc.sw,
				sweepLimits{discoverPages: 1}); err != nil {
				t.Fatalf("collectIDs: %v", err)
			}
			for _, call := range f.calls {
				if strings.HasPrefix(call, "/discover/") && !strings.Contains(call, tc.want) {
					t.Fatalf("discover call %q lacks %q", call, tc.want)
				}
			}
		})
	}
}

// News, soap and talk are daily formats, not what anyone puts on tonight.
func TestTVSweep_SkipsDailyFormats(t *testing.T) {
	for _, g := range []int{10763, 10766, 10767} {
		if slices.Contains(tvSweep.genres, g) {
			t.Errorf("TV sweep walks genre %d", g)
		}
	}
}

func TestCollectIDs_StopsAtTheCap(t *testing.T) {
	f := &fakeLists{}
	ids, err := collectIDs(context.Background(), f, tvSweep,
		sweepLimits{popularPages: 1, topRatedPages: 1, discoverPages: 1, minVotes: 20, max: 5})
	if err != nil {
		t.Fatalf("collectIDs: %v", err)
	}
	if len(ids) != 5 {
		t.Errorf("collected %d ids, want the cap of 5", len(ids))
	}
}

func countOf(ids []int, id int) int {
	n := 0
	for _, v := range ids {
		if v == id {
			n++
		}
	}
	return n
}
