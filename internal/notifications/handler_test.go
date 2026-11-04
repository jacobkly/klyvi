package notifications_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/notifications"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/users"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env.dev")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip(".env.dev not found; skipping DB-dependent test")
		}
		dir = parent
	}
}

// openDB connects to the test database, or skips when there is none.
func openDB(t *testing.T) *sqlx.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	if err := godotenv.Load(filepath.Join(findRepoRoot(t), ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}
	conn, err := db.New(*config.NewDB())
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// newUser creates a user the test owns. Deleting it takes its notices along.
func newUser(t *testing.T, conn *sqlx.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := users.NewRepository(conn).EnsureUser(context.Background(), id); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `delete from users where id = $1`, id)
	})
	return id
}

type seedNotice struct {
	user   uuid.UUID
	tmdbID int
	title  string
	age    time.Duration
	read   bool
}

// seed writes a film notice directly, so a test can set how old it is. A
// non-zero tmdbID keeps it apart from the others under the unique key.
func seed(t *testing.T, conn *sqlx.DB, n seedNotice) int64 {
	t.Helper()
	createdAt := time.Now().Add(-n.age)
	var readAt *time.Time
	if n.read {
		readAt = &createdAt
	}
	var id int64
	err := conn.QueryRowContext(context.Background(), `
		insert into notifications (user_id, kind, media_type, tmdb_id, title, event_date, created_at, read_at)
		values ($1, 'film_release', 'movie', $2, $3, current_date, $4, $5)
		returning id
	`, n.user, n.tmdbID, n.title, createdAt, readAt).Scan(&id)
	if err != nil {
		t.Fatalf("seed notice: %v", err)
	}
	return id
}

func api(conn *sqlx.DB) *notifications.API {
	return notifications.NewAPI(notifications.NewService(notifications.NewRepository(conn)))
}

type pageBody struct {
	Data struct {
		Notifications []notifications.Notification `json:"notifications"`
		UnreadCount   int                          `json:"unread_count"`
		HasMore       bool                         `json:"has_more"`
	} `json:"data"`
}

func list(t *testing.T, conn *sqlx.DB, user uuid.UUID, query string) pageBody {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/notifications"+query, nil)
	req = req.WithContext(middleware.WithUserUUID(req.Context(), user))
	rec := httptest.NewRecorder()
	api(conn).List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body pageBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode page: %v, body %s", err, rec.Body.String())
	}
	return body
}

func markRead(conn *sqlx.DB, user uuid.UUID, reqBody string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/notifications/read", strings.NewReader(reqBody))
	req = req.WithContext(middleware.WithUserUUID(req.Context(), user))
	rec := httptest.NewRecorder()
	api(conn).MarkRead(rec, req)
	return rec
}

func titles(ns []notifications.Notification) string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Title)
	}
	return strings.Join(out, ",")
}

func TestList_PagesNewestFirst(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	seed(t, conn, seedNotice{user: user, tmdbID: 1, title: "oldest", age: 3 * time.Hour})
	seed(t, conn, seedNotice{user: user, tmdbID: 2, title: "middle", age: 2 * time.Hour})
	seed(t, conn, seedNotice{user: user, tmdbID: 3, title: "newest", age: time.Hour})

	cases := []struct {
		name       string
		query      string
		wantTitles string
		wantMore   bool
	}{
		{"first page has more", "?limit=2", "newest,middle", true},
		{"last page does not", "?limit=2&offset=2", "oldest", false},
		{"a page that ends exactly at the last row", "?limit=3", "newest,middle,oldest", false},
		{"past the end is empty", "?limit=2&offset=9", "", false},
		{"the default fits them all", "", "newest,middle,oldest", false},
		{"nonsense falls back to the default", "?limit=abc&offset=-4", "newest,middle,oldest", false},
		{"a limit below one falls back too", "?limit=0", "newest,middle,oldest", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := list(t, conn, user, tc.query)
			if titles(got.Data.Notifications) != tc.wantTitles {
				t.Errorf("titles = %q, want %q", titles(got.Data.Notifications), tc.wantTitles)
			}
			if got.Data.HasMore != tc.wantMore {
				t.Errorf("has_more = %v, want %v", got.Data.HasMore, tc.wantMore)
			}
			if got.Data.UnreadCount != 3 {
				t.Errorf("unread_count = %d, want 3 on every page", got.Data.UnreadCount)
			}
		})
	}
}

// An oversized limit is cut down rather than refused, so it can't ask for
// more than one page's worth of work.
func TestList_CapsTheLimit(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	for i := 1; i <= notifications.MaxPageSize+1; i++ {
		seed(t, conn, seedNotice{user: user, tmdbID: i, title: "t"})
	}

	got := list(t, conn, user, "?limit=100000")

	if len(got.Data.Notifications) != notifications.MaxPageSize {
		t.Errorf("got %d rows, want the cap of %d", len(got.Data.Notifications), notifications.MaxPageSize)
	}
	if !got.Data.HasMore {
		t.Error("has_more = false, want true with a row left over")
	}
}

// The client maps over this list, so no notices has to encode as [].
func TestList_EmptyIsAnArray(t *testing.T) {
	conn := openDB(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/notifications", nil)
	req = req.WithContext(middleware.WithUserUUID(req.Context(), newUser(t, conn)))
	rec := httptest.NewRecorder()

	api(conn).List(rec, req)

	if !strings.Contains(rec.Body.String(), `"notifications":[]`) {
		t.Errorf("body = %s, want an empty array", rec.Body.String())
	}
}

func TestList_UnreadCountsOnlyThisUsersUnreadNotices(t *testing.T) {
	conn := openDB(t)
	user, other := newUser(t, conn), newUser(t, conn)
	seed(t, conn, seedNotice{user: user, tmdbID: 1, title: "unread"})
	seed(t, conn, seedNotice{user: user, tmdbID: 2, title: "read", read: true})
	seed(t, conn, seedNotice{user: other, tmdbID: 1, title: "someone else's"})

	got := list(t, conn, user, "")

	if got.Data.UnreadCount != 1 {
		t.Errorf("unread_count = %d, want 1", got.Data.UnreadCount)
	}
	if titles(got.Data.Notifications) != "read,unread" && titles(got.Data.Notifications) != "unread,read" {
		t.Errorf("titles = %q, want only this user's two", titles(got.Data.Notifications))
	}
}

// A season notice shows the season's own poster, and the show's when the
// season has none. A film notice shows the film's.
func TestList_FillsThePosterFromTheCatalog(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	ctx := context.Background()
	const movieID, seriesID = 990001, 990002
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, `delete from movies where movie_id = $1`, movieID)
		_, _ = conn.ExecContext(ctx, `delete from tv_seasons where tv_id = $1`, seriesID)
		_, _ = conn.ExecContext(ctx, `delete from tv_series where tv_id = $1`, seriesID)
	})
	for _, q := range []struct {
		sql string
		id  int
	}{
		{`insert into movies (movie_id, poster_path) values ($1, '/film.jpg')`, movieID},
		{`insert into tv_series (tv_id, poster_path) values ($1, '/show.jpg')`, seriesID},
		{`insert into tv_seasons (tv_id, season_number, poster_path) values ($1, 2, '/season.jpg')`, seriesID},
	} {
		if _, err := conn.ExecContext(ctx, q.sql, q.id); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}
	insert := `insert into notifications (user_id, kind, media_type, tmdb_id, season_number, title, event_date)
		values ($1, $2, $3, $4, $5, $6, current_date)`
	for _, n := range []struct {
		kind, media string
		id          int
		season      *int
		title       string
	}{
		{"film_release", "movie", movieID, nil, "film"},
		{"season_premiere", "tv", seriesID, intPtr(2), "season with poster"},
		{"season_premiere", "tv", seriesID, intPtr(3), "season without poster"},
		{"film_release", "movie", 990003, nil, "uncached film"},
	} {
		if _, err := conn.ExecContext(ctx, insert, user, n.kind, n.media, n.id, n.season, n.title); err != nil {
			t.Fatalf("seed notice: %v", err)
		}
	}

	want := map[string]string{
		"film":                  "/film.jpg",
		"season with poster":    "/season.jpg",
		"season without poster": "/show.jpg",
		"uncached film":         "",
	}
	for _, n := range list(t, conn, user, "").Data.Notifications {
		got := ""
		if n.PosterPath != nil {
			got = *n.PosterPath
		}
		if got != want[n.Title] {
			t.Errorf("%s: poster = %q, want %q", n.Title, got, want[n.Title])
		}
		if len(n.EventDate) != len("2026-01-02") {
			t.Errorf("%s: event_date = %q, want a plain date", n.Title, n.EventDate)
		}
	}
}

func intPtr(i int) *int { return &i }

func unreadFrom(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("mark read: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			UnreadCount int `json:"unread_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Data.UnreadCount
}

func TestMarkRead_One(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	first := seed(t, conn, seedNotice{user: user, tmdbID: 1, title: "first"})
	seed(t, conn, seedNotice{user: user, tmdbID: 2, title: "second"})

	body, _ := json.Marshal(map[string]any{"ids": []int64{first}})
	if got := unreadFrom(t, markRead(conn, user, string(body))); got != 1 {
		t.Errorf("unread_count = %d, want 1", got)
	}

	for _, n := range list(t, conn, user, "").Data.Notifications {
		if (n.ID == first) != (n.ReadAt != nil) {
			t.Errorf("%s: read_at = %v, only the first should be read", n.Title, n.ReadAt)
		}
	}
}

func TestMarkRead_All(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	seed(t, conn, seedNotice{user: user, tmdbID: 1, title: "first"})
	seed(t, conn, seedNotice{user: user, tmdbID: 2, title: "second"})

	if got := unreadFrom(t, markRead(conn, user, `{"all":true}`)); got != 0 {
		t.Errorf("unread_count = %d, want 0", got)
	}
}

// Marking again must not move the time a notice was first read, since the
// 90-day cleanup counts from it.
func TestMarkRead_KeepsTheFirstReadTime(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	id := seed(t, conn, seedNotice{user: user, tmdbID: 1, title: "read a while ago", age: 48 * time.Hour, read: true})
	before := list(t, conn, user, "").Data.Notifications[0].ReadAt

	unreadFrom(t, markRead(conn, user, `{"all":true}`))
	body, _ := json.Marshal(map[string]any{"ids": []int64{id}})
	unreadFrom(t, markRead(conn, user, string(body)))

	after := list(t, conn, user, "").Data.Notifications[0].ReadAt
	if before == nil || after == nil || !before.Equal(*after) {
		t.Errorf("read_at moved from %v to %v", before, after)
	}
}

// Ids come from the client, so they are only honoured for the caller's own
// notices. Someone else's are skipped without an error that would confirm
// they exist.
func TestMarkRead_IgnoresAnotherUsersIDs(t *testing.T) {
	conn := openDB(t)
	user, other := newUser(t, conn), newUser(t, conn)
	theirs := seed(t, conn, seedNotice{user: other, tmdbID: 1, title: "theirs"})
	seed(t, conn, seedNotice{user: user, tmdbID: 1, title: "mine"})

	body, _ := json.Marshal(map[string]any{"ids": []int64{theirs}})
	if got := unreadFrom(t, markRead(conn, user, string(body))); got != 1 {
		t.Errorf("caller's unread_count = %d, want 1", got)
	}

	if got := list(t, conn, other, "").Data.UnreadCount; got != 1 {
		t.Errorf("owner's unread_count = %d, want it still unread", got)
	}
}

func TestMarkRead_RefusesABadBody(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	seed(t, conn, seedNotice{user: user, tmdbID: 1, title: "still unread"})

	for _, body := range []string{``, `not json`, `{}`, `{"ids":[]}`, `{"all":false}`, `{"ids":[1],"all":true}`} {
		t.Run(body, func(t *testing.T) {
			rec := markRead(conn, user, body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
	}
	if got := list(t, conn, user, "").Data.UnreadCount; got != 1 {
		t.Errorf("unread_count = %d, a refused request must change nothing", got)
	}
}

func TestHandlers_RequireASignedInUser(t *testing.T) {
	a := notifications.NewAPI(nil)
	cases := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"list", a.List},
		{"mark read", a.MarkRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"all":true}`)))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

// A database fault must not reach the client as text.
func TestHandlers_HideDatabaseErrors(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	a := api(conn)
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	cases := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"list", a.List},
		{"mark read", a.MarkRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"all":true}`))
			req = req.WithContext(middleware.WithUserUUID(req.Context(), user))
			rec := httptest.NewRecorder()
			tc.call(rec, req)
			if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal server error") {
				t.Errorf("status %d, body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUniqueKey_ANoticeExistsOnce(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	cases := []struct {
		name         string
		kind, media  string
		tmdbID       int
		season       *int
		secondSeason *int
		wantSecond   int64
	}{
		{"the same film twice", "film_release", "movie", 1, nil, nil, 0},
		{"the same season twice", "season_premiere", "tv", 2, intPtr(1), intPtr(1), 0},
		{"another season of the show", "season_premiere", "tv", 3, intPtr(1), intPtr(2), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			insert := func(season *int) int64 {
				res, err := conn.ExecContext(context.Background(), `
					insert into notifications (user_id, kind, media_type, tmdb_id, season_number, title, event_date)
					values ($1, $2, $3, $4, $5, 'x', current_date)
					on conflict do nothing
				`, user, tc.kind, tc.media, tc.tmdbID, season)
				if err != nil {
					t.Fatalf("insert: %v", err)
				}
				n, _ := res.RowsAffected()
				return n
			}
			if got := insert(tc.season); got != 1 {
				t.Fatalf("first insert wrote %d rows", got)
			}
			if got := insert(tc.secondSeason); got != tc.wantSecond {
				t.Errorf("second insert wrote %d rows, want %d", got, tc.wantSecond)
			}
		})
	}
}

func TestAccountDeletion_RemovesTheNotices(t *testing.T) {
	conn := openDB(t)
	user := newUser(t, conn)
	seed(t, conn, seedNotice{user: user, tmdbID: 1, title: "goes with the account"})

	if err := users.NewRepository(conn).DeleteAccountData(context.Background(), user); err != nil {
		t.Fatalf("DeleteAccountData: %v", err)
	}

	var left int
	if err := conn.GetContext(context.Background(), &left,
		`select count(*) from notifications where user_id = $1`, user); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Errorf("%d notices left after deleting the account", left)
	}
}
