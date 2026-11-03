package tv_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/tracking"
	"klyvi-api/internal/tv"
	"klyvi-api/internal/users"
)

// fakeTMDB serves canned series and season payloads for any id, decoded
// from JSON so numbers arrive as float64 the way the real client returns
// them. It records every endpoint it is asked for.
type fakeTMDB struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeTMDB) TMDBRequest(_ context.Context, _, endpoint string, _ interface{}) (map[string]interface{}, error) {
	f.mu.Lock()
	f.calls = append(f.calls, endpoint)
	f.mu.Unlock()

	var id, season int
	var payload string
	switch {
	case strings.Contains(endpoint, "/season/"):
		if _, err := fmt.Sscanf(endpoint, "/tv/%d/season/%d", &id, &season); err != nil {
			return nil, err
		}
		payload = fmt.Sprintf(`{
			"season_number": %d, "name": "Season %d", "air_date": "2008-01-20",
			"episodes": [{"episode_number": 1, "name": "Pilot", "runtime": 58}]
		}`, season, season)
	case strings.HasPrefix(endpoint, "/tv/"):
		if _, err := fmt.Sscanf(endpoint, "/tv/%d", &id); err != nil {
			return nil, err
		}
		payload = fmt.Sprintf(`{
			"id": %d, "name": "Klyvi test series", "vote_average": 8.1, "vote_count": 900,
			"genres": [{"id": 18, "name": "Drama"}, {"id": 80, "name": "Crime"}],
			"keywords": {"results": [{"id": 1, "name": "a"}, {"id": 2, "name": "b"}]},
			"credits": {"cast": [{"id": 17419, "name": "Bryan Cranston", "order": 0}], "crew": []},
			"seasons": [{"season_number": 0, "episode_count": 3}, {"season_number": 1, "episode_count": 7}]
		}`, id)
	default:
		return nil, errors.New("fakeTMDB: unexpected endpoint " + endpoint)
	}

	var out map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// seriesCalls counts requests for the series detail itself, not its seasons.
func (f *fakeTMDB) seriesCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if !strings.Contains(c, "/season/") {
			n++
		}
	}
	return n
}

// resetShow deletes every catalog row of a synthetic show, now and after the
// test.
func resetShow(t *testing.T, dbh *sqlx.DB, tvID int) {
	t.Helper()
	reset := func() {
		cleanupExec(dbh, `delete from media_index where id = $1 and media_type in ('tv', 'season')`, tvID)
		cleanupExec(dbh, `delete from tv_seasons where tv_id = $1`, tvID)
		cleanupExec(dbh, `delete from tv_series where tv_id = $1`, tvID)
	}
	reset()
	t.Cleanup(reset)
}

// seriesState is what the catalog holds for one show: its keyword ids (empty
// when the series row is missing or has none) and whether its 'tv'
// media_index row exists.
func seriesState(t *testing.T, dbh *sqlx.DB, tvID int) (keywords string, indexed bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := dbh.QueryRowContext(ctx, `
		select
			coalesce((select array_to_string(keyword_ids, ',') from tv_series where tv_id = $1), ''),
			exists (select 1 from media_index where id = $1 and media_type = 'tv')
	`, tvID).Scan(&keywords, &indexed); err != nil {
		t.Fatalf("read series state: %v", err)
	}
	return keywords, indexed
}

// Task 2.2's acceptance: tracking a season of a show nobody has opened yet
// leaves the series cached with its keywords and its own identity row, so
// signal on the season can roll up to it.
func TestTracking_SeasonOfAnUncachedShowCachesTheSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_010
	resetShow(t, dbh, tvID)
	userID := uuid.New()
	if err := users.NewRepository(dbh).EnsureUser(ctx, userID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	t.Cleanup(func() {
		cleanupExec(dbh, `delete from media_list where user_id = $1`, userID)
		cleanupExec(dbh, `delete from users where id = $1`, userID)
	})

	series := tv.NewService(&fakeTMDB{}, tv.NewRepository(dbh))
	svc := tracking.NewService(tracking.NewRepository(dbh)).WithSeriesEnsurer(series)
	one := 1
	if _, err := svc.Add(ctx, userID, tracking.AddRequest{
		TMDBID: tvID, MediaType: tracking.MediaTypeSeason, SeasonNumber: &one,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	keywords, indexed := seriesState(t, dbh, tvID)
	if keywords != "1,2" || !indexed {
		t.Errorf("series keywords = {%s}, indexed = %v; want {1,2} and true", keywords, indexed)
	}
}

// EnsureSeries fetches a series only once. After that the cache answers.
func TestEnsureSeries_FetchesOnlyWhenTheSeriesIsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_011
	resetShow(t, dbh, tvID)
	client := &fakeTMDB{}
	svc := tv.NewService(client, tv.NewRepository(dbh))

	for range 2 {
		if err := svc.EnsureSeries(ctx, tvID); err != nil {
			t.Fatalf("EnsureSeries: %v", err)
		}
	}

	if n := client.seriesCalls(); n != 1 {
		t.Errorf("series fetched %d times, want 1", n)
	}
	keywords, indexed := seriesState(t, dbh, tvID)
	if keywords != "1,2" || !indexed {
		t.Errorf("series keywords = {%s}, indexed = %v; want {1,2} and true", keywords, indexed)
	}
}

// An import needs each season's episode count to tell a finished season
// from one in progress. The counts come from the cached series, which is
// fetched once.
func TestSeasonEpisodeCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_015
	resetShow(t, dbh, tvID)
	client := &fakeTMDB{}
	svc := tv.NewService(client, tv.NewRepository(dbh))

	for range 2 {
		counts, err := svc.SeasonEpisodeCounts(ctx, tvID)
		if err != nil {
			t.Fatalf("SeasonEpisodeCounts: %v", err)
		}
		if want := map[int]int{0: 3, 1: 7}; !reflect.DeepEqual(counts, want) {
			t.Errorf("counts = %v, want %v", counts, want)
		}
	}
	if n := client.seriesCalls(); n != 1 {
		t.Errorf("series fetched %d times, want 1", n)
	}
}

// Opening a series or one of its seasons leaves the series cached and
// indexed either way.
func TestGetTvById_CachesAndIndexesTheSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	requireMigration(t, dbh, 30)

	tests := []struct {
		name   string
		tvID   int
		season int
	}{
		{"series page", 2_100_000_012, 0},
		{"season page", 2_100_000_013, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resetShow(t, dbh, tt.tvID)
			svc := tv.NewService(&fakeTMDB{}, tv.NewRepository(dbh))

			if _, err := svc.GetTvById(ctx, "external", tt.tvID, tt.season); err != nil {
				t.Fatalf("GetTvById: %v", err)
			}

			keywords, indexed := seriesState(t, dbh, tt.tvID)
			if keywords != "1,2" || !indexed {
				t.Errorf("series keywords = {%s}, indexed = %v; want {1,2} and true", keywords, indexed)
			}
		})
	}
}

// A full detail record replaces a series cached without keywords or credits,
// and a partial record never replaces a full one.
func TestInsertTVSeries_FullRecordReplacesPartialOne(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_014
	resetShow(t, dbh, tvID)
	keywords := json.RawMessage(`[{"id": 1, "name": "a"}]`)
	credits := json.RawMessage(`{"cast": [], "crew": []}`)
	partial := func(name string) *tv.TVSeries { return &tv.TVSeries{TVID: tvID, Name: &name} }
	full := func(name string) *tv.TVSeries {
		return &tv.TVSeries{TVID: tvID, Name: &name, Keywords: &keywords, Credits: &credits}
	}

	steps := []struct {
		write *tv.TVSeries
		want  string
	}{
		{partial("from a list"), "from a list"},
		{full("from detail"), "from detail"},
		{partial("from a later list"), "from detail"},
	}
	for _, step := range steps {
		if err := repo.InsertTVSeries(ctx, step.write); err != nil {
			t.Fatalf("InsertTVSeries(%s): %v", *step.write.Name, err)
		}
		got, err := repo.GetTVSeriesByID(ctx, tvID)
		if err != nil || got == nil {
			t.Fatalf("GetTVSeriesByID: %v, %v", got, err)
		}
		if *got.Name != step.want {
			t.Errorf("after writing %q the row is %q, want %q", *step.write.Name, *got.Name, step.want)
		}
	}
}
