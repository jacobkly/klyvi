package tv_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tv"
)

// The refresh tests backdate their rows to the 1990s and look for stale rows
// before 2000. Real rows are younger than that, so the tests only ever see
// their own.
var (
	staleCutoff = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	in1990      = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	in1991      = time.Date(1991, 1, 1, 0, 0, 0, 0, time.UTC)
)

// failingTMDB answers like fakeTMDB, except that it fails every request with
// err when err is set.
type failingTMDB struct {
	fakeTMDB
	err error
}

func (f *failingTMDB) TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.fakeTMDB.TMDBRequest(ctx, method, endpoint, body)
}

func backdate(t *testing.T, dbh *sqlx.DB, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := dbh.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

// cacheShow caches a series and its first season through the service, the
// way a user opening them would.
func cacheShow(t *testing.T, svc *tv.Service, tvID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := svc.GetTvById(ctx, "external", tvID, 1); err != nil {
		t.Fatalf("cache show %d: %v", tvID, err)
	}
}

func seriesIDs(rows []tv.StaleSeries, ids ...int) []int {
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var got []int
	for _, r := range rows {
		if want[r.TVID] {
			got = append(got, r.TVID)
		}
	}
	return got
}

func seasonShows(rows []tv.StaleSeason, ids ...int) []int {
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var got []int
	for _, r := range rows {
		if want[r.TVID] {
			got = append(got, r.TVID)
		}
	}
	return got
}

func TestListStale_SeriesAndSeasonsOldestFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const older, old, fresh = 2_100_000_050, 2_100_000_051, 2_100_000_052
	svc := tv.NewService(&fakeTMDB{}, repo)
	for _, id := range []int{older, old, fresh} {
		resetShow(t, dbh, id)
		cacheShow(t, svc, id)
	}
	for id, at := range map[int]time.Time{older: in1990, old: in1991} {
		backdate(t, dbh, `update tv_series set updated_at = $2 where tv_id = $1`, id, at)
		backdate(t, dbh, `update tv_seasons set updated_at = $2 where tv_id = $1`, id, at)
	}

	series, err := repo.ListStaleSeries(ctx, staleCutoff, 1000)
	if err != nil {
		t.Fatalf("ListStaleSeries: %v", err)
	}
	if got := seriesIDs(series, older, old, fresh); len(got) != 2 || got[0] != older || got[1] != old {
		t.Errorf("stale series = %v, want [%d %d]", got, older, old)
	}

	seasons, err := repo.ListStaleSeasons(ctx, staleCutoff, 1000)
	if err != nil {
		t.Fatalf("ListStaleSeasons: %v", err)
	}
	if got := seasonShows(seasons, older, old, fresh); len(got) != 2 || got[0] != older || got[1] != old {
		t.Errorf("stale seasons belong to %v, want [%d %d]", got, older, old)
	}
	for _, s := range seasons {
		if s.TVID == older && s.SeasonNumber != 1 {
			t.Errorf("stale season number = %d, want 1", s.SeasonNumber)
		}
	}
}

func TestRefreshSeriesAndSeason_OverwriteAndStamp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_053
	resetShow(t, dbh, tvID)
	svc := tv.NewService(&fakeTMDB{}, repo)
	cacheShow(t, svc, tvID)
	backdate(t, dbh, `update tv_series set name = 'Old name', updated_at = $2 where tv_id = $1`, tvID, in1990)
	backdate(t, dbh, `update tv_seasons set name = 'Old season', updated_at = $2 where tv_id = $1`, tvID, in1990)

	if removed, err := svc.RefreshSeries(ctx, tvID); err != nil || removed {
		t.Fatalf("RefreshSeries = %v, %v; want false, nil", removed, err)
	}
	if removed, err := svc.RefreshSeason(ctx, tvID, 1); err != nil || removed {
		t.Fatalf("RefreshSeason = %v, %v; want false, nil", removed, err)
	}

	var seriesName, seasonName string
	var seriesAt, seasonAt time.Time
	if err := dbh.QueryRowContext(ctx, `
		select s.name, s.updated_at, se.name, se.updated_at
		from tv_series s join tv_seasons se on se.tv_id = s.tv_id and se.season_number = 1
		where s.tv_id = $1
	`, tvID).Scan(&seriesName, &seriesAt, &seasonName, &seasonAt); err != nil {
		t.Fatalf("read show: %v", err)
	}
	if seriesName != "Klyvi test series" || seasonName != "Season 1" {
		t.Errorf("names = %q, %q; want TMDB's current ones", seriesName, seasonName)
	}
	if time.Since(seriesAt) > time.Hour || time.Since(seasonAt) > time.Hour {
		t.Errorf("updated_at = %v, %v; want both stamped now", seriesAt, seasonAt)
	}
}

// TMDB removed the show. Its cached series and seasons go, and its
// media_index rows stay for whoever tracked it.
func TestRefreshSeriesAndSeason_RemovedFromTMDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const gone, seasonGone = 2_100_000_054, 2_100_000_055
	for _, id := range []int{gone, seasonGone} {
		resetShow(t, dbh, id)
		cacheShow(t, tv.NewService(&fakeTMDB{}, repo), id)
	}
	svc := tv.NewService(&failingTMDB{err: &tmdb.StatusError{Status: http.StatusNotFound}}, repo)

	if removed, err := svc.RefreshSeries(ctx, gone); err != nil || !removed {
		t.Fatalf("RefreshSeries = %v, %v; want true, nil", removed, err)
	}
	if removed, err := svc.RefreshSeason(ctx, seasonGone, 1); err != nil || !removed {
		t.Fatalf("RefreshSeason = %v, %v; want true, nil", removed, err)
	}

	var seriesLeft, seasonsLeft, indexLeft int
	if err := dbh.QueryRowContext(ctx, `
		select
			(select count(*) from tv_series where tv_id = $1),
			(select count(*) from tv_seasons where tv_id = $1),
			(select count(*) from media_index where id = $1 and media_type in ('tv', 'season'))
	`, gone).Scan(&seriesLeft, &seasonsLeft, &indexLeft); err != nil {
		t.Fatalf("count removed show: %v", err)
	}
	if seriesLeft != 0 || seasonsLeft != 0 || indexLeft != 2 {
		t.Errorf("removed show: %d series, %d seasons, %d index rows; want 0, 0, 2", seriesLeft, seasonsLeft, indexLeft)
	}

	if err := dbh.QueryRowContext(ctx, `
		select
			(select count(*) from tv_series where tv_id = $1),
			(select count(*) from tv_seasons where tv_id = $1)
	`, seasonGone).Scan(&seriesLeft, &seasonsLeft); err != nil {
		t.Fatalf("count removed season: %v", err)
	}
	if seriesLeft != 1 || seasonsLeft != 0 {
		t.Errorf("removed season: %d series, %d seasons; want the series kept and the season gone", seriesLeft, seasonsLeft)
	}
}

// A refresh that fails for any other reason writes nothing and passes the
// status on, so the caller can tell throttling from an outage.
func TestRefreshSeries_OtherFailuresWriteNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_056
	resetShow(t, dbh, tvID)
	cacheShow(t, tv.NewService(&fakeTMDB{}, repo), tvID)

	for _, tmdbErr := range []error{&tmdb.StatusError{Status: http.StatusTooManyRequests}, errors.New("connection reset")} {
		svc := tv.NewService(&failingTMDB{err: tmdbErr}, repo)
		removed, err := svc.RefreshSeries(ctx, tvID)
		if removed || !errors.Is(err, tmdbErr) {
			t.Errorf("RefreshSeries = %v, %v; want false and %v", removed, err, tmdbErr)
		}
		removed, err = svc.RefreshSeason(ctx, tvID, 1)
		if removed || !errors.Is(err, tmdbErr) {
			t.Errorf("RefreshSeason = %v, %v; want false and %v", removed, err, tmdbErr)
		}
	}
	if keywords, _ := seriesState(t, dbh, tvID); keywords != "1,2" {
		t.Errorf("series keywords = {%s}, want the cached {1,2} untouched", keywords)
	}
}
