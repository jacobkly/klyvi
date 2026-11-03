package imports

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/interactions"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tracking"
)

// fakeCatalog knows how many episodes each season of a few shows has. Any
// other show fails, the way an unknown TMDB id does.
type fakeCatalog struct {
	seasons map[int]map[int]int

	mu    sync.Mutex
	calls int
}

func (f *fakeCatalog) SeasonEpisodeCounts(_ context.Context, tvID int) (map[int]int, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	counts, ok := f.seasons[tvID]
	if !ok {
		return nil, &tmdb.StatusError{Status: http.StatusNotFound}
	}
	return counts, nil
}

// fakeMovies caches every film except the ones TMDB no longer has.
type fakeMovies struct{ missing map[int]bool }

func (f fakeMovies) EnsureCached(_ context.Context, tmdbID int) error {
	if f.missing[tmdbID] {
		return fmt.Errorf("fetch movie %d: %w", tmdbID, &tmdb.StatusError{Status: http.StatusNotFound})
	}
	return nil
}

// fakeTracker records every entry the worker adds.
type fakeTracker struct {
	mu   sync.Mutex
	adds []tracking.AddRequest
}

func (f *fakeTracker) Add(_ context.Context, _ uuid.UUID, req tracking.AddRequest) (*tracking.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds = append(f.adds, req)
	return &tracking.Entry{}, nil
}

// fakeSignals records every interaction the worker writes.
type fakeSignals struct {
	mu      sync.Mutex
	records []interactions.RecordRequest
}

func (f *fakeSignals) Record(_ context.Context, _ uuid.UUID, req interactions.RecordRequest) (*interactions.Interaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, req)
	return &interactions.Interaction{}, nil
}

// entry is one tracked title, flattened so a list of them compares and
// prints as plain data.
type entry struct {
	TMDBID   int
	Media    string
	Season   int
	Status   string
	Score    int // -1 when unscored
	Progress int // -1 when unset
}

func (f *fakeTracker) entries() []entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []entry{}
	for _, a := range f.adds {
		e := entry{TMDBID: a.TMDBID, Media: a.MediaType, Score: -1, Progress: -1}
		if a.SeasonNumber != nil {
			e.Season = *a.SeasonNumber
		}
		if a.Status != nil {
			e.Status = *a.Status
		}
		if a.Score != nil {
			e.Score = *a.Score
		}
		if a.EpisodeProgress != nil {
			e.Progress = *a.EpisodeProgress
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TMDBID != out[j].TMDBID {
			return out[i].TMDBID < out[j].TMDBID
		}
		return out[i].Season < out[j].Season
	})
	return out
}

// signal is one rated interaction, flattened the same way.
type signal struct {
	TMDBID int
	Media  string
	Season int
	Rating int
}

func (f *fakeSignals) signals(t *testing.T) []signal {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []signal{}
	for _, r := range f.records {
		if r.Kind != interactions.KindRated || r.Rating == nil {
			t.Errorf("interaction %+v is not a rating", r)
			continue
		}
		s := signal{TMDBID: r.TMDBID, Media: r.MediaType, Rating: *r.Rating}
		if r.SeasonNumber != nil {
			s.Season = *r.SeasonNumber
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TMDBID != out[j].TMDBID {
			return out[i].TMDBID < out[j].TMDBID
		}
		return out[i].Season < out[j].Season
	})
	return out
}

// showImport runs one import to completion against the fakes and returns
// the finished job.
func showImport(t *testing.T, svc *Service, repo *fakeRepo, source Source, data []byte) *Import {
	t.Helper()
	imp, err := svc.Start(context.Background(), uuid.New(), string(source), data)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForStatus(t, repo, imp.ID, StatusDone)
	got, _ := repo.Get(context.Background(), uuid.Nil, imp.ID)
	return got
}

func assertCounts(t *testing.T, got *Import, total, matched, unmatched int) {
	t.Helper()
	if got.Total != total || got.Matched != matched || got.Unmatched != unmatched {
		t.Errorf("counts total=%d matched=%d unmatched=%d, want %d/%d/%d",
			got.Total, got.Matched, got.Unmatched, total, matched, unmatched)
	}
}

func assertEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %+v\nwant %+v", what, got, want)
	}
}

// The trimmed Trakt export lands as the right seasons with the right status
// and progress. A finished season is Completed with every episode counted,
// a part-watched one is Watching, and ratings follow their title: a season
// rating onto the season, a show rating as a series signal.
func TestRun_TraktExportTracksSeasons(t *testing.T) {
	repo := newFakeRepo()
	search := &searchTMDB{}
	tracker := &fakeTracker{}
	signals := &fakeSignals{}
	catalog := &fakeCatalog{seasons: map[int]map[int]int{
		87108: {1: 5},        // Chernobyl
		95396: {1: 9, 2: 10}, // Severance
	}}
	svc := NewService(repo, search, fakeMovies{}, tracker, signals).WithSeries(catalog)

	got := showImport(t, svc, repo, SourceTrakt, zipDir(t, "testdata/trakt-export"))

	assertCounts(t, got, 6, 5, 1)
	assertEqual(t, "unmatched", got.Titles, UnmatchedTitles{{Title: "Obscure Short", Year: 2003}})
	assertEqual(t, "tracked", tracker.entries(), []entry{
		{TMDBID: 87108, Media: "season", Season: 1, Status: "completed", Score: 100, Progress: 5},
		{TMDBID: 95396, Media: "season", Season: 1, Status: "watching", Score: -1, Progress: 3},
		{TMDBID: 329865, Media: "movie", Status: "completed", Score: -1, Progress: -1},
		{TMDBID: 438631, Media: "movie", Status: "completed", Score: 90, Progress: -1},
	})
	assertEqual(t, "rated", signals.signals(t), []signal{
		{TMDBID: 87108, Media: "season", Season: 1, Rating: 100},
		{TMDBID: 95396, Media: "tv", Rating: 80},
		{TMDBID: 438631, Media: "movie", Rating: 90},
	})
	// Only the film with no TMDB id was searched. Everything else came with one.
	assertEqual(t, "searched", search.endpoints(), []string{
		"/search/movie?query=Obscure+Short&language=en-US&page=1&year=2003",
		"/search/movie?query=Obscure+Short&language=en-US&page=1",
	})
	// Two shows, two catalog reads: Severance's show rating and season share one.
	if catalog.calls != 2 {
		t.Errorf("catalog reads = %d, want 2", catalog.calls)
	}
}

// A CSV TV row with a season tracks that season. A weak match, a season the
// show does not have and an id TMDB does not know all go to unmatched
// instead of tracking a guess.
func TestRun_CSVShowRows(t *testing.T) {
	repo := newFakeRepo()
	search := &searchTMDB{results: map[string][]any{
		"Severance":  {tvResult(95396, "Severance", "Severance", "2022-02-17")},
		"Severence":  {tvResult(95396, "Severance", "Severance", "2022-02-17")},
		"The Office": {tvResult(2996, "The Office", "The Office", "2001-07-09"), tvResult(2316, "The Office", "The Office", "2005-03-24")},
	}}
	tracker := &fakeTracker{}
	signals := &fakeSignals{}
	catalog := &fakeCatalog{seasons: map[int]map[int]int{
		87108: {1: 5},
		94605: {1: 9, 2: 9},
		95396: {1: 9, 2: 10},
	}}
	svc := NewService(repo, search, fakeMovies{missing: map[int]bool{111: true}}, tracker, signals).WithSeries(catalog)

	file := "type,title,year,season,tmdb_id,rating\n" +
		"tv,Severance,2022,1,,90\n" + // a strong search match
		"tv,The Office,,1,,\n" + // two shows by that name and no year
		"tv,Severence,2022,2,,\n" + // the name is misspelled
		"tv,Chernobyl,2019,1,87108,\n" + // an id, so no search
		"tv,Chernobyl,2019,7,87108,\n" + // a season the show does not have
		"tv,Lost Show,2010,1,999999,\n" + // an id TMDB does not know
		"tv,Arcane,2021,,94605,85\n" + // a show rating with no season
		"movie,Dune,2021,,438631,90\n" + // a film with an id
		"movie,Gone,2000,,111,\n" // a film TMDB no longer has

	got := showImport(t, svc, repo, SourceCSV, []byte(file))

	assertCounts(t, got, 9, 4, 5)
	assertEqual(t, "unmatched", got.Titles, UnmatchedTitles{
		{Title: "The Office, season 1"},
		{Title: "Severence, season 2", Year: 2022},
		{Title: "Chernobyl, season 7", Year: 2019},
		{Title: "Lost Show, season 1", Year: 2010},
		{Title: "Gone", Year: 2000},
	})
	assertEqual(t, "tracked", tracker.entries(), []entry{
		{TMDBID: 87108, Media: "season", Season: 1, Status: "completed", Score: -1, Progress: 5},
		{TMDBID: 95396, Media: "season", Season: 1, Status: "completed", Score: 90, Progress: 9},
		{TMDBID: 438631, Media: "movie", Status: "completed", Score: 90, Progress: -1},
	})
	assertEqual(t, "rated", signals.signals(t), []signal{
		{TMDBID: 94605, Media: "tv", Rating: 85},
		{TMDBID: 95396, Media: "season", Season: 1, Rating: 90},
		{TMDBID: 438631, Media: "movie", Rating: 90},
	})
	for _, e := range search.endpoints() {
		if !strings.HasPrefix(e, "/search/tv?") {
			t.Errorf("searched %s, want only show searches", e)
		}
	}
	if n := len(search.endpoints()); n != 3 {
		t.Errorf("searches = %d, want 3 (the rows with no id)", n)
	}
}

// Without a series catalog wired, show rows are unmatched rather than lost
// or guessed.
func TestRun_ShowRowsWithoutACatalogAreUnmatched(t *testing.T) {
	repo := newFakeRepo()
	tracker := &fakeTracker{}
	svc := NewService(repo, &searchTMDB{}, fakeMovies{}, tracker, &fakeSignals{})

	got := showImport(t, svc, repo, SourceCSV, []byte("type,title,season,tmdb_id\ntv,Chernobyl,1,87108\n"))

	assertCounts(t, got, 1, 0, 1)
	if n := len(tracker.entries()); n != 0 {
		t.Errorf("tracked %d entries, want none", n)
	}
}

// The row cap counts titles after the episodes roll up, so a Trakt export
// over the cap is refused before any job exists.
func TestStart_TraktExportRowCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := range maxImportRows + 1 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"rated_at": "2024-01-01T00:00:00.000Z", "rating": 7, "type": "movie",
			"movie": {"title": "Film %d", "year": 2000, "ids": {"tmdb": %d}}}`, i, i+1)
	}
	b.WriteString("]")
	export := zipFiles(t, map[string]string{"ratings-movies.json": b.String()})

	repo := newFakeRepo()
	svc := NewService(repo, noResultsTMDB{}, nil, nil, nil)
	_, err := svc.Start(context.Background(), uuid.New(), string(SourceTrakt), export)
	if !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("Start error = %v, want %v", err, ErrTooManyRows)
	}
	if n := repo.count(); n != 0 {
		t.Errorf("created %d jobs, want none", n)
	}
}
