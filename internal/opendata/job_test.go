package opendata

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeStore struct {
	ids   map[string][]int
	saved map[string][][]int // batches saved per media type
}

func (s *fakeStore) CatalogIDs(_ context.Context, mt string) ([]int, error) { return s.ids[mt], nil }

func (s *fakeStore) Save(_ context.Context, mt string, batch []int, facts []Facts) (int, error) {
	if s.saved == nil {
		s.saved = map[string][][]int{}
	}
	s.saved[mt] = append(s.saved[mt], append([]int(nil), batch...))
	return len(facts), nil
}

// fakeSource matches every even id and fails any batch holding failID.
type fakeSource struct{ failID int }

func (f fakeSource) Facts(_ context.Context, mt string, ids []int) ([]Facts, error) {
	var out []Facts
	for _, id := range ids {
		if id == f.failID {
			return nil, errors.New("service down")
		}
		if id%2 == 0 {
			out = append(out, Facts{MediaType: mt, TMDBID: id})
		}
	}
	return out, nil
}

func TestJobRun(t *testing.T) {
	store := &fakeStore{ids: map[string][]int{"movie": {1, 2, 3, 4, 5}, "tv": {10, 11}}}
	res, err := NewJob(store, fakeSource{}, 2, 0).Run(context.Background(), MediaTypes)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (Result{Checked: 7, Matched: 3, Changed: 3}); res != want {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	want := map[string][][]int{"movie": {{1, 2}, {3, 4}, {5}}, "tv": {{10, 11}}}
	if !reflect.DeepEqual(store.saved, want) {
		t.Errorf("saved batches = %v, want %v", store.saved, want)
	}
}

// One failed batch is skipped, the rest still run, and the run reports it.
func TestJobRun_KeepsGoingPastAFailedBatch(t *testing.T) {
	store := &fakeStore{ids: map[string][]int{"movie": {1, 2, 3, 4}}}
	res, err := NewJob(store, fakeSource{failID: 1}, 2, 0).Run(context.Background(), []string{"movie"})
	if err == nil {
		t.Fatal("want an error for the failed batch")
	}
	if want := (Result{Checked: 2, Matched: 1, Changed: 1, Failed: 1}); res != want {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	if want := [][]int{{3, 4}}; !reflect.DeepEqual(store.saved["movie"], want) {
		t.Errorf("saved = %v, want only the good batch %v", store.saved["movie"], want)
	}
}
