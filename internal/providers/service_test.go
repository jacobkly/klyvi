package providers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/providers"
)

type fakeTMDB struct {
	endpoints []string
	payload   map[string]interface{}
	err       error
}

func (f *fakeTMDB) TMDBRequest(_ context.Context, _, endpoint string, _ interface{}) (map[string]interface{}, error) {
	f.endpoints = append(f.endpoints, endpoint)
	if f.err != nil {
		return nil, f.err
	}
	return f.payload, nil
}

type fakeStore struct {
	copy *providers.Copy
	puts int
}

func (s *fakeStore) Get(_ context.Context, _ string, _ int) (*providers.Copy, error) {
	return s.copy, nil
}

func (s *fakeStore) Put(_ context.Context, _ string, _ int, results json.RawMessage) error {
	s.puts++
	s.copy = &providers.Copy{Results: results, FetchedAt: time.Now()}
	return nil
}

// severance is TMDB's watch/providers answer, cut down to two regions.
func severance() map[string]interface{} {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(`{
	  "id": 95396,
	  "results": {
	    "US": {
	      "link": "https://www.themoviedb.org/tv/95396-severance/watch?locale=US",
	      "flatrate": [
	        {"provider_id": 350, "provider_name": "Apple TV+", "logo_path": "/apple.jpg", "display_priority": 2},
	        {"provider_id": 2, "provider_name": "Apple TV", "logo_path": "/itunes.jpg", "display_priority": 1}
	      ],
	      "buy": [{"provider_id": 2, "provider_name": "Apple TV", "logo_path": "/itunes.jpg", "display_priority": 1}]
	    },
	    "GB": {
	      "link": "javascript:alert(1)",
	      "flatrate": [{"provider_id": 350, "provider_name": "Apple TV+", "logo_path": "/apple.jpg", "display_priority": 4}]
	    }
	  }
	}`), &m)
	return m
}

func TestGet_PicksTheRegionAndListsTheOthers(t *testing.T) {
	api := &fakeTMDB{payload: severance()}
	store := &fakeStore{}
	svc := providers.NewService(api, store)

	got, err := svc.Get(context.Background(), "tv", 95396, "US")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !slices.Equal(api.endpoints, []string{"/tv/95396/watch/providers"}) {
		t.Errorf("endpoints %v", api.endpoints)
	}
	if got.Region != "US" || !slices.Equal(got.Regions, []string{"GB", "US"}) {
		t.Errorf("region %q regions %v", got.Region, got.Regions)
	}
	if got.Link == nil || *got.Link != "https://www.themoviedb.org/tv/95396-severance/watch?locale=US" {
		t.Errorf("link %v", got.Link)
	}
	// Ordered by TMDB's display priority.
	if len(got.Stream) != 2 || got.Stream[0].Name != "Apple TV" || got.Stream[1].Name != "Apple TV+" {
		t.Errorf("stream %+v", got.Stream)
	}
	if len(got.Buy) != 1 || len(got.Rent) != 0 || got.Rent == nil {
		t.Errorf("buy %+v rent %+v (rent must be an empty list, not null)", got.Buy, got.Rent)
	}
	if store.puts != 1 {
		t.Errorf("stored %d times, want 1", store.puts)
	}

	gb, err := svc.Get(context.Background(), "tv", 95396, "GB")
	if err != nil {
		t.Fatalf("Get GB: %v", err)
	}
	if len(api.endpoints) != 1 {
		t.Errorf("a fresh copy refetched: %v", api.endpoints)
	}
	if gb.Link != nil {
		t.Errorf("a link that isn't TMDB's was kept: %v", *gb.Link)
	}

	none, err := svc.Get(context.Background(), "tv", 95396, "FR")
	if err != nil {
		t.Fatalf("Get FR: %v", err)
	}
	if none.Region != "FR" || len(none.Stream)+len(none.Buy) != 0 {
		t.Errorf("FR: %+v, want nothing listed", none)
	}
}

func stored(t *testing.T, age time.Duration) *fakeStore {
	t.Helper()
	raw, _ := json.Marshal(severance()["results"])
	return &fakeStore{copy: &providers.Copy{Results: raw, FetchedAt: time.Now().Add(-age)}}
}

func TestGet_RefetchesACopyOverAWeekOld(t *testing.T) {
	cases := []struct {
		name    string
		age     time.Duration
		fetches int
	}{
		{"six days old", 6 * 24 * time.Hour, 0},
		{"eight days old", 8 * 24 * time.Hour, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeTMDB{payload: severance()}
			store := stored(t, tc.age)
			if _, err := providers.NewService(api, store).Get(context.Background(), "movie", 1, "US"); err != nil {
				t.Fatalf("Get: %v", err)
			}
			if len(api.endpoints) != tc.fetches || store.puts != tc.fetches {
				t.Errorf("fetched %d, stored %d, want %d", len(api.endpoints), store.puts, tc.fetches)
			}
		})
	}
}

func TestGet_ServesAStaleCopyWhenTMDBFails(t *testing.T) {
	api := &fakeTMDB{err: errors.New("tmdb down")}
	store := stored(t, 30*24*time.Hour)
	got, err := providers.NewService(api, store).Get(context.Background(), "tv", 95396, "US")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Stream) != 2 {
		t.Errorf("stream %+v, want the stale copy", got.Stream)
	}
}

func TestGet_NotFound(t *testing.T) {
	api := &fakeTMDB{err: &tmdb.StatusError{Status: http.StatusNotFound}}
	_, err := providers.NewService(api, &fakeStore{}).Get(context.Background(), "movie", 9, "US")
	if !errors.Is(err, providers.ErrNotFound) {
		t.Errorf("err %v, want ErrNotFound", err)
	}
}
