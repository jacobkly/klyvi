package notifications_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/notifications"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tv"
)

// jobToday is the job's clock in these tests. Every date below is an offset
// from it, so the windows are tested exactly.
var jobToday = time.Date(2026, 6, 15, 3, 0, 0, 0, time.UTC)

func dayOffset(days int) string {
	return jobToday.AddDate(0, 0, days).Format("2006-01-02")
}

// fakeTMDB answers the job's refetches from canned detail records, and writes
// down what it was asked for.
type fakeTMDB struct {
	series map[int]map[string]any
	films  map[int]map[string]any
	errs   map[string]error
	calls  []string
}

func newFakeTMDB() *fakeTMDB {
	return &fakeTMDB{series: map[int]map[string]any{}, films: map[int]map[string]any{}, errs: map[string]error{}}
}

func (f *fakeTMDB) TMDBRequest(_ context.Context, _, endpoint string, _ interface{}) (map[string]interface{}, error) {
	path, _, _ := strings.Cut(endpoint, "?")
	f.calls = append(f.calls, path)
	if err := f.errs[path]; err != nil {
		return nil, err
	}
	var id int
	if _, err := fmt.Sscanf(path, "/tv/%d", &id); err == nil {
		if raw, ok := f.series[id]; ok {
			return raw, nil
		}
	}
	if _, err := fmt.Sscanf(path, "/movie/%d", &id); err == nil {
		if raw, ok := f.films[id]; ok {
			return raw, nil
		}
	}
	return nil, &tmdb.StatusError{Status: http.StatusNotFound}
}

func (f *fakeTMDB) callCount(path string) int {
	count := 0
	for _, c := range f.calls {
		if c == path {
			count++
		}
	}
	return count
}

// airing is one season as TMDB lists it. An empty date is a season with no
// announced date.
type airing struct {
	number int
	date   string
}

func (f *fakeTMDB) addSeries(id int, name string, seasons ...airing) {
	list := make([]any, 0, len(seasons))
	for _, s := range seasons {
		season := map[string]any{"season_number": float64(s.number), "air_date": nil}
		if s.date != "" {
			season["air_date"] = s.date
		}
		list = append(list, season)
	}
	f.series[id] = map[string]any{"id": float64(id), "name": name, "seasons": list}
}

func (f *fakeTMDB) addFilm(id int, title, releaseDate string) {
	f.films[id] = map[string]any{"id": float64(id), "title": title, "release_date": releaseDate}
}

// onlyOwned keeps a test away from whatever else a local database holds. The
// job walks every tracked title, and it skips one reported as removed, so
// titles this test didn't create are never refetched or written to.
type onlyOwned struct {
	owned  map[int]bool
	series *tv.Service
	films  *movies.Service
}

func (o onlyOwned) RefreshSeries(ctx context.Context, id int) (bool, error) {
	if !o.owned[id] {
		return true, nil
	}
	return o.series.RefreshSeries(ctx, id)
}

func (o onlyOwned) RefreshMovie(ctx context.Context, id int) (bool, error) {
	if !o.owned[id] {
		return true, nil
	}
	return o.films.RefreshMovie(ctx, id)
}

// jobFixture owns a range of TMDB ids, and removes whatever it wrote for them.
type jobFixture struct {
	t     *testing.T
	conn  *sqlx.DB
	tmdb  *fakeTMDB
	owned map[int]bool
}

func newJobFixture(t *testing.T, conn *sqlx.DB) *jobFixture {
	t.Helper()
	f := &jobFixture{t: t, conn: conn, tmdb: newFakeTMDB(), owned: map[int]bool{}}
	return f
}

// own claims a TMDB id for this test and cleans up after it.
func (f *jobFixture) own(id int) int {
	f.t.Helper()
	f.owned[id] = true
	ctx := context.Background()
	f.t.Cleanup(func() {
		_, _ = f.conn.ExecContext(ctx, `delete from media_list where media_id in (select media_id from media_index where id = $1 and media_type in ('season', 'movie'))`, id)
		_, _ = f.conn.ExecContext(ctx, `delete from media_index where id = $1 and media_type in ('season', 'movie')`, id)
		_, _ = f.conn.ExecContext(ctx, `delete from tv_series where tv_id = $1`, id)
		_, _ = f.conn.ExecContext(ctx, `delete from movies where movie_id = $1`, id)
		_, _ = f.conn.ExecContext(ctx, `delete from notifications where tmdb_id = $1`, id)
	})
	return id
}

func (f *jobFixture) job() *notifications.Job {
	owned := onlyOwned{
		owned:  f.owned,
		series: tv.NewService(f.tmdb, tv.NewRepository(f.conn)),
		films:  movies.NewService(f.tmdb, movies.NewRepository(f.conn)),
	}
	job := notifications.NewJob(notifications.NewRepository(f.conn), owned, owned)
	job.Now = func() time.Time { return jobToday }
	job.ThrottlePause = 0
	return job
}

func (f *jobFixture) run() notifications.JobResult {
	f.t.Helper()
	res, err := f.job().Run(context.Background())
	if err != nil {
		f.t.Fatalf("Run: %v", err)
	}
	return res
}

type trackOpts struct {
	status  string
	deleted bool
}

// track puts one row on a user's list. A nil season means a film.
func (f *jobFixture) track(user uuid.UUID, id int, season *int, opts trackOpts) {
	f.t.Helper()
	mediaType := "movie"
	if season != nil {
		mediaType = "season"
	}
	ctx := context.Background()
	var mediaID int
	err := f.conn.QueryRowContext(ctx, `
		insert into media_index (id, season_number, media_type) values ($1, $2, $3)
		on conflict (id, season_number, media_type) do update set id = excluded.id
		returning media_id
	`, id, season, mediaType).Scan(&mediaID)
	if err != nil {
		f.t.Fatalf("seed media_index: %v", err)
	}
	if _, err := f.conn.ExecContext(ctx, `
		insert into media_list (user_id, media_id, media_type, status, is_deleted)
		values ($1, $2, $3, $4, $5)
	`, user, mediaID, mediaType, opts.status, opts.deleted); err != nil {
		f.t.Fatalf("seed media_list: %v", err)
	}
}

func (f *jobFixture) prefs(user uuid.UUID, premieres, releases bool) {
	f.t.Helper()
	if _, err := f.conn.ExecContext(context.Background(),
		`update users set notify_premieres = $2, notify_releases = $3 where id = $1`,
		user, premieres, releases); err != nil {
		f.t.Fatalf("set prefs: %v", err)
	}
}

// noticesOf reads a user's notices, oldest season first.
func noticesOf(t *testing.T, conn *sqlx.DB, user uuid.UUID) []string {
	t.Helper()
	got := []string{}
	err := conn.SelectContext(context.Background(), &got, `
		select kind || '|' || media_type || '|' || tmdb_id || '|' || coalesce(season_number::text, '-') || '|' || title || '|' || event_date::text
		from notifications where user_id = $1 order by tmdb_id, season_number
	`, user)
	if err != nil {
		t.Fatalf("read notices: %v", err)
	}
	return got
}

func TestJob_SeasonPremieres(t *testing.T) {
	conn := openDB(t)

	const (
		active   = "watching"
		finished = "completed"
		dropped  = "dropped"
	)
	type row struct {
		season  int
		status  string
		deleted bool
	}
	type tracker struct {
		rows         []row
		premieresOff bool
		want         []int // the seasons this user should be told about
	}
	cases := []struct {
		name     string
		seasons  []airing
		trackers []tracker
	}{
		{
			name:    "a new season inside the window creates one notice per tracking user",
			seasons: []airing{{1, dayOffset(-400)}, {2, dayOffset(3)}},
			trackers: []tracker{
				{rows: []row{{1, finished, false}}, want: []int{2}},
				{rows: []row{{1, active, false}}, want: []int{2}},
			},
		},
		{
			name:     "a season the user already tracks creates none",
			seasons:  []airing{{1, dayOffset(-400)}, {2, dayOffset(3)}},
			trackers: []tracker{{rows: []row{{1, finished, false}, {2, "planning", false}}}},
		},
		{
			name:     "a dropped series creates none",
			seasons:  []airing{{1, dayOffset(-400)}, {2, dayOffset(3)}},
			trackers: []tracker{{rows: []row{{1, dropped, false}}}},
		},
		{
			name:     "dropping the latest season tracked counts as dropping the series",
			seasons:  []airing{{1, dayOffset(-800)}, {2, dayOffset(-400)}, {3, dayOffset(3)}},
			trackers: []tracker{{rows: []row{{1, finished, false}, {2, dropped, false}}}},
		},
		{
			name:     "an old dropped season does not hide a later one",
			seasons:  []airing{{1, dayOffset(-800)}, {2, dayOffset(-400)}, {3, dayOffset(3)}},
			trackers: []tracker{{rows: []row{{1, dropped, false}, {2, active, false}}, want: []int{3}}},
		},
		{
			name:    "turning premieres off stops that user's notices only",
			seasons: []airing{{1, dayOffset(-400)}, {2, dayOffset(3)}},
			trackers: []tracker{
				{rows: []row{{1, finished, false}}, premieresOff: true},
				{rows: []row{{1, finished, false}}, want: []int{2}},
			},
		},
		{
			name:     "a removed list entry doesn't count as tracking",
			seasons:  []airing{{1, dayOffset(-400)}, {2, dayOffset(3)}},
			trackers: []tracker{{rows: []row{{1, finished, true}}}},
		},
		{
			name:     "specials are skipped",
			seasons:  []airing{{0, dayOffset(1)}, {1, dayOffset(-400)}},
			trackers: []tracker{{rows: []row{{1, finished, false}}}},
		},
		{
			name:     "seven days ahead is inside the window",
			seasons:  []airing{{1, dayOffset(-400)}, {2, dayOffset(7)}},
			trackers: []tracker{{rows: []row{{1, finished, false}}, want: []int{2}}},
		},
		{
			name:     "eight days ahead is not",
			seasons:  []airing{{1, dayOffset(-400)}, {2, dayOffset(8)}},
			trackers: []tracker{{rows: []row{{1, finished, false}}}},
		},
		{
			name:     "thirty days ago is inside the window",
			seasons:  []airing{{1, dayOffset(-400)}, {2, dayOffset(-30)}},
			trackers: []tracker{{rows: []row{{1, finished, false}}, want: []int{2}}},
		},
		{
			name:     "thirty-one days ago is not",
			seasons:  []airing{{1, dayOffset(-400)}, {2, dayOffset(-31)}},
			trackers: []tracker{{rows: []row{{1, finished, false}}}},
		},
		{
			name:     "a season with no date is not",
			seasons:  []airing{{1, dayOffset(-400)}, {2, ""}},
			trackers: []tracker{{rows: []row{{1, finished, false}}}},
		},
		{
			name:     "two new seasons make two notices",
			seasons:  []airing{{1, dayOffset(-400)}, {2, dayOffset(-5)}, {3, dayOffset(5)}},
			trackers: []tracker{{rows: []row{{1, finished, false}}, want: []int{2, 3}}},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newJobFixture(t, conn)
			id := f.own(900970000 + i)
			f.tmdb.addSeries(id, "The Show", tc.seasons...)
			users := make([]uuid.UUID, len(tc.trackers))
			for u, tr := range tc.trackers {
				users[u] = newUser(t, conn)
				f.prefs(users[u], !tr.premieresOff, true)
				for _, r := range tr.rows {
					f.track(users[u], id, intPtr(r.season), trackOpts{status: r.status, deleted: r.deleted})
				}
			}

			f.run()

			air := map[int]string{}
			for _, s := range tc.seasons {
				air[s.number] = s.date
			}
			for u, tr := range tc.trackers {
				want := []string{}
				for _, season := range tr.want {
					want = append(want, fmt.Sprintf("season_premiere|tv|%d|%d|The Show|%s", id, season, air[season]))
				}
				if got := noticesOf(t, conn, users[u]); !slices.Equal(got, want) {
					t.Errorf("user %d: notices = %v, want %v", u, got, want)
				}
			}
		})
	}
}

func TestJob_FilmReleases(t *testing.T) {
	conn := openDB(t)

	cases := []struct {
		name       string
		release    string
		status     string
		deleted    bool
		releaseOff bool
		wantNotice bool
	}{
		{name: "released today", release: dayOffset(0), status: "planning", wantNotice: true},
		{name: "released thirty days ago", release: dayOffset(-30), status: "planning", wantNotice: true},
		{name: "released thirty-one days ago", release: dayOffset(-31), status: "planning"},
		{name: "out tomorrow", release: dayOffset(1), status: "planning"},
		{name: "no release date", release: "", status: "planning"},
		{name: "only the planning list counts", release: dayOffset(-1), status: "completed"},
		{name: "a removed list entry doesn't count", release: dayOffset(-1), status: "planning", deleted: true},
		{name: "turning releases off stops it", release: dayOffset(-1), status: "planning", releaseOff: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newJobFixture(t, conn)
			id := f.own(900971000 + i)
			f.tmdb.addFilm(id, "The Film", tc.release)
			user := newUser(t, conn)
			f.prefs(user, true, !tc.releaseOff)
			f.track(user, id, nil, trackOpts{status: tc.status, deleted: tc.deleted})

			f.run()

			want := []string{}
			if tc.wantNotice {
				want = append(want, fmt.Sprintf("film_release|movie|%d|-|The Film|%s", id, tc.release))
			}
			if got := noticesOf(t, conn, user); !slices.Equal(got, want) {
				t.Errorf("notices = %v, want %v", got, want)
			}
		})
	}
}

// Everyone planning a film hears about it once, from a single fetch.
func TestJob_FilmNoticeForEachPlanner(t *testing.T) {
	conn := openDB(t)
	f := newJobFixture(t, conn)
	id := f.own(900972000)
	f.tmdb.addFilm(id, "The Film", dayOffset(-2))
	first, second := newUser(t, conn), newUser(t, conn)
	f.track(first, id, nil, trackOpts{status: "planning"})
	f.track(second, id, nil, trackOpts{status: "planning"})

	f.run()

	for name, user := range map[string]uuid.UUID{"first": first, "second": second} {
		if got := noticesOf(t, conn, user); len(got) != 1 {
			t.Errorf("%s: notices = %v, want one", name, got)
		}
	}
	if got := f.tmdb.callCount(fmt.Sprintf("/movie/%d", id)); got != 1 {
		t.Errorf("TMDB calls = %d, want 1", got)
	}
}

// The job is what keeps these rows fresh, so it refetches on what is tracked,
// and only what the plan calls for. A date TMDB has moved is what it acts on.
func TestJob_WhatItRefetches(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	f := newJobFixture(t, conn)
	user := newUser(t, conn)

	const series, droppedSeries, offBoth = 900973000, 900973001, 900973002
	const nearFilm, farFilm, datelessFilm, uncachedFilm, unplannedFilm = 900973010, 900973011, 900973012, 900973013, 900973014
	for _, id := range []int{series, droppedSeries, offBoth, nearFilm, farFilm, datelessFilm, uncachedFilm, unplannedFilm} {
		f.own(id)
	}
	f.tmdb.addSeries(series, "Tracked", airing{1, dayOffset(-400)})
	f.track(user, series, intPtr(1), trackOpts{status: "watching"})
	f.tmdb.addSeries(droppedSeries, "Dropped", airing{1, dayOffset(-400)})
	f.track(user, droppedSeries, intPtr(1), trackOpts{status: "dropped"})

	silent := newUser(t, conn)
	f.prefs(silent, false, false)
	f.tmdb.addSeries(offBoth, "Nobody wants notices", airing{1, dayOffset(-400)})
	f.track(silent, offBoth, intPtr(1), trackOpts{status: "watching"})

	for _, film := range []struct {
		id      int
		release *string
		cached  bool
	}{
		{nearFilm, new(dayOffset(60)), true},
		{farFilm, new(dayOffset(61)), true},
		{datelessFilm, nil, true},
		{uncachedFilm, nil, false},
	} {
		if film.cached {
			if _, err := conn.ExecContext(ctx,
				`insert into movies (movie_id, title, release_date) values ($1, 'cached', $2)`,
				film.id, film.release); err != nil {
				t.Fatalf("seed movie: %v", err)
			}
		}
		f.track(user, film.id, nil, trackOpts{status: "planning"})
	}
	// TMDB has since brought this one forward to today.
	f.tmdb.addFilm(nearFilm, "Moved up", dayOffset(0))
	f.tmdb.addFilm(datelessFilm, "Still undated", "")
	f.tmdb.addFilm(uncachedFilm, "Not cached yet", dayOffset(90))
	f.tmdb.addFilm(unplannedFilm, "On nobody's list", dayOffset(0))

	f.run()

	cases := []struct {
		path string
		want int
	}{
		{fmt.Sprintf("/tv/%d", series), 1},
		{fmt.Sprintf("/tv/%d", droppedSeries), 0},
		{fmt.Sprintf("/tv/%d", offBoth), 1},
		{fmt.Sprintf("/movie/%d", nearFilm), 1},
		{fmt.Sprintf("/movie/%d", farFilm), 0},
		{fmt.Sprintf("/movie/%d", datelessFilm), 1},
		{fmt.Sprintf("/movie/%d", uncachedFilm), 1},
		{fmt.Sprintf("/movie/%d", unplannedFilm), 0},
	}
	for _, tc := range cases {
		if got := f.tmdb.callCount(tc.path); got != tc.want {
			t.Errorf("%s: %d calls, want %d", tc.path, got, tc.want)
		}
	}

	want := []string{fmt.Sprintf("film_release|movie|%d|-|Moved up|%s", nearFilm, dayOffset(0))}
	if got := noticesOf(t, conn, user); !slices.Equal(got, want) {
		t.Errorf("notices = %v, want %v", got, want)
	}
}

func TestJob_RunningTwiceCreatesNothingNew(t *testing.T) {
	conn := openDB(t)
	f := newJobFixture(t, conn)
	series, film := f.own(900974000), f.own(900974001)
	f.tmdb.addSeries(series, "The Show", airing{1, dayOffset(-400)}, airing{2, dayOffset(3)})
	f.tmdb.addFilm(film, "The Film", dayOffset(-1))
	user := newUser(t, conn)
	f.track(user, series, intPtr(1), trackOpts{status: "completed"})
	f.track(user, film, nil, trackOpts{status: "planning"})

	first := f.run()
	unreadAfterFirst := list(t, conn, user, "").Data.UnreadCount
	unreadFrom(t, markRead(conn, user, `{"all":true}`))
	second := f.run()

	if first.Created != 2 {
		t.Errorf("first run created %d, want 2", first.Created)
	}
	if unreadAfterFirst != 2 {
		t.Errorf("unread after the first run = %d, want 2", unreadAfterFirst)
	}
	if second.Created != 0 {
		t.Errorf("second run created %d, want 0", second.Created)
	}
	if got := list(t, conn, user, "").Data.UnreadCount; got != 0 {
		t.Errorf("unread after the second run = %d, a re-run must not bring a read notice back", got)
	}
}

func TestJob_ATitleTMDBDroppedCreatesNothing(t *testing.T) {
	conn := openDB(t)
	f := newJobFixture(t, conn)
	id := f.own(900975000)
	user := newUser(t, conn)
	f.track(user, id, intPtr(1), trackOpts{status: "watching"})

	res := f.run()

	if got := noticesOf(t, conn, user); len(got) != 0 {
		t.Errorf("notices = %v, want none", got)
	}
	if res.Failed != 0 {
		t.Errorf("failed = %d, TMDB answering 404 is not a failure", res.Failed)
	}
}

// One title failing costs that title, and the next night tries it again.
func TestJob_AFailedFetchSkipsOnlyThatTitle(t *testing.T) {
	conn := openDB(t)
	f := newJobFixture(t, conn)
	broken, working := f.own(900976000), f.own(900976001)
	f.tmdb.errs[fmt.Sprintf("/tv/%d", broken)] = errors.New("boom")
	f.tmdb.addSeries(working, "Works", airing{1, dayOffset(-400)}, airing{2, dayOffset(3)})
	user := newUser(t, conn)
	f.track(user, broken, intPtr(1), trackOpts{status: "watching"})
	f.track(user, working, intPtr(1), trackOpts{status: "watching"})

	res := f.run()

	if res.Failed != 1 {
		t.Errorf("failed = %d, want 1", res.Failed)
	}
	want := []string{fmt.Sprintf("season_premiere|tv|%d|2|Works|%s", working, dayOffset(3))}
	if got := noticesOf(t, conn, user); !slices.Equal(got, want) {
		t.Errorf("notices = %v, want %v", got, want)
	}
}

// A run of failures points at an outage. Carrying on would only burn requests.
func TestJob_StopsAfterTooManyFailuresInARow(t *testing.T) {
	conn := openDB(t)
	f := newJobFixture(t, conn)
	user := newUser(t, conn)
	ids := []int{f.own(900977000), f.own(900977001), f.own(900977002), f.own(900977003)}
	for _, id := range ids {
		f.tmdb.errs[fmt.Sprintf("/tv/%d", id)] = errors.New("outage")
		f.track(user, id, intPtr(1), trackOpts{status: "watching"})
	}
	job := f.job()
	job.MaxFailedInARow = 3

	res, err := job.Run(context.Background())

	if err == nil {
		t.Fatal("Run returned no error")
	}
	if res.Failed != 3 {
		t.Errorf("failed = %d, want 3", res.Failed)
	}
}

// TMDB asking the job to slow down gets a pause rather than a stream of retries.
func TestJob_PausesWhenTMDBThrottles(t *testing.T) {
	conn := openDB(t)
	f := newJobFixture(t, conn)
	id := f.own(900978000)
	f.tmdb.errs[fmt.Sprintf("/tv/%d", id)] = &tmdb.StatusError{Status: http.StatusTooManyRequests}
	f.track(newUser(t, conn), id, intPtr(1), trackOpts{status: "watching"})
	job := f.job()
	const pause = 60 * time.Millisecond
	job.ThrottlePause = pause

	start := time.Now()
	if _, err := job.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if elapsed := time.Since(start); elapsed < pause {
		t.Errorf("run took %s, want at least the %s pause", elapsed, pause)
	}
}

// A user can delete their account while the job runs. That isn't a failure,
// and it must not stop anyone else's notices.
func TestInsert_AUserWhoIsGoneCreatesNothing(t *testing.T) {
	conn := openDB(t)
	created, err := notifications.NewRepository(conn).Insert(context.Background(), notifications.NewNotice{
		UserID:    uuid.New(),
		Kind:      notifications.KindFilmRelease,
		MediaType: "movie",
		TMDBID:    1,
		Title:     "x",
		EventDate: "2026-01-02",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if created {
		t.Error("created = true, want false")
	}
}

func TestJob_Housekeeping(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	const day = 24 * time.Hour
	cases := []struct {
		title string
		age   time.Duration
		read  bool
		kept  bool
	}{
		{"read 91 days ago", 91 * day, true, false},
		{"read 89 days ago", 89 * day, true, true},
		{"unread at 100 days", 100 * day, false, true},
		{"unread at 169 days", 169 * day, false, true},
		// The stored title came from TMDB, so it can't outlive the cache limit.
		{"unread at 171 days", 171 * day, false, false},
	}
	for i, tc := range cases {
		seed(t, conn, seedNotice{user: user, tmdbID: i + 1, title: tc.title, age: tc.age, read: tc.read})
	}

	job := notifications.NewJob(notifications.NewRepository(conn), onlyOwned{}, onlyOwned{})
	res, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Deleted < 2 {
		t.Errorf("deleted = %d, want at least this test's 2", res.Deleted)
	}
	left := map[string]bool{}
	for _, n := range list(t, conn, user, "").Data.Notifications {
		left[n.Title] = true
	}
	for _, tc := range cases {
		if left[tc.title] != tc.kept {
			t.Errorf("%s: kept = %v, want %v", tc.title, left[tc.title], tc.kept)
		}
	}
}

// One failure between successes isn't an outage.
func TestJob_ASuccessResetsTheFailureStreak(t *testing.T) {
	conn := openDB(t)
	f := newJobFixture(t, conn)
	user := newUser(t, conn)
	ids := []int{f.own(900979000), f.own(900979001), f.own(900979002), f.own(900979003)}
	for i, id := range ids {
		if i%2 == 0 {
			f.tmdb.errs[fmt.Sprintf("/tv/%d", id)] = errors.New("flaky")
		} else {
			f.tmdb.addSeries(id, "Works", airing{1, dayOffset(-400)})
		}
		f.track(user, id, intPtr(1), trackOpts{status: "watching"})
	}
	job := f.job()
	job.MaxFailedInARow = 2

	res, err := job.Run(context.Background())

	if err != nil {
		t.Fatalf("Run stopped: %v", err)
	}
	if res.Failed != 2 {
		t.Errorf("failed = %d, want 2", res.Failed)
	}
}

// A notice with no name would show as a blank row in the list.
func TestJob_ATitleWithNoNameCreatesNothing(t *testing.T) {
	conn := openDB(t)
	f := newJobFixture(t, conn)
	series, film := f.own(900979010), f.own(900979011)
	f.tmdb.addSeries(series, "", airing{1, dayOffset(-400)}, airing{2, dayOffset(3)})
	f.tmdb.addFilm(film, "", dayOffset(-1))
	user := newUser(t, conn)
	f.track(user, series, intPtr(1), trackOpts{status: "watching"})
	f.track(user, film, nil, trackOpts{status: "planning"})

	f.run()

	if got := noticesOf(t, conn, user); len(got) != 0 {
		t.Errorf("notices = %v, want none", got)
	}
}
