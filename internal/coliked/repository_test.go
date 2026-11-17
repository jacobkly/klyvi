package coliked

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joeshaw/envdecode"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/reco"
)

// TMDB ids far above any real one, so the test never touches real rows.
const (
	film1 = 2_000_000_101 + iota
	film2
	film3
	show
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	var cfg config.ConfDB
	if err := envdecode.StrictDecode(&cfg); err != nil {
		t.Skipf("DB env incomplete: %v", err)
	}
	conn, err := db.New(cfg)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	return conn
}

// TestRebuild counts three hand-made histories:
//
//	u1 likes film1, film2, film3 and season 1 of show
//	u2 likes film1 and logs film2 without rating it
//	u3 rated film1 highly then down to 30, likes film2, logs season 1
//
// so film1 has two likers, film2 three, film3 one and show two, and only
// film1 with film2 and film2 with show are liked together by two users.
func TestRebuild(t *testing.T) {
	conn := testDB(t)
	ctx := context.Background()
	users := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	cleanup := func() {
		for _, u := range users {
			_, _ = conn.ExecContext(ctx, `delete from users where id = $1`, u)
		}
		_, _ = conn.ExecContext(ctx, `delete from media_index where id between $1 and $2`, film1, show)
		_, _ = conn.ExecContext(ctx, `delete from co_liked`)
	}
	cleanup()
	t.Cleanup(func() { cleanup(); _ = conn.Close() })

	index := func(id int, kind string, season *int) int {
		t.Helper()
		var mid int
		if err := conn.GetContext(ctx, &mid, `
			insert into media_index (id, season_number, media_type) values ($1, $2, $3) returning media_id`,
			id, season, kind); err != nil {
			t.Fatalf("index %d: %v", id, err)
		}
		return mid
	}
	one := 1
	m1, m2, m3 := index(film1, "movie", nil), index(film2, "movie", nil), index(film3, "movie", nil)
	series, season := index(show, "tv", nil), index(show, "season", &one)

	for i, u := range users {
		if _, err := conn.ExecContext(ctx, `insert into users (id, username) values ($1, $2)`,
			u, "coliked-test-"+u.String()[:8]+string(rune('a'+i))); err != nil {
			t.Fatalf("user: %v", err)
		}
	}
	at := time.Now().Add(-time.Hour)
	add := func(u uuid.UUID, mid int, mediaType, kind string, rating any) {
		t.Helper()
		at = at.Add(time.Minute)
		if _, err := conn.ExecContext(ctx, `
			insert into interactions (user_id, media_id, media_type, kind, rating, created_at)
			values ($1, $2, $3, $4, $5, $6)`, u, mid, mediaType, kind, rating, at); err != nil {
			t.Fatalf("interaction: %v", err)
		}
	}
	u1, u2, u3 := users[0], users[1], users[2]
	add(u1, m1, "movie", "rated", 90)
	add(u1, m2, "movie", "rated", 80)
	add(u1, m3, "movie", "rated", 75)
	add(u1, season, "season", "rated", 85)
	add(u2, m1, "movie", "rated", 70)
	add(u2, m2, "movie", "logged", nil)
	add(u2, m3, "movie", "dismissed", nil)
	add(u3, m1, "movie", "rated", 95)
	add(u3, m1, "movie", "rated", 30)
	add(u3, m2, "movie", "rated", 100)
	add(u3, season, "season", "logged", nil)
	add(u3, m3, "movie", "impression", nil)

	repo := NewRepository(conn)
	ours := []int{m1, m2, m3, series, season}
	read := func(o Options) map[int][]reco.CoLiked {
		t.Helper()
		if _, err := repo.Rebuild(ctx, o); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
		got, err := repo.CoLiked(ctx, ours)
		if err != nil {
			t.Fatalf("CoLiked: %v", err)
		}
		return got
	}

	sim := 2 / math.Sqrt(6)
	want := map[int][]reco.CoLiked{
		m1:     {{ID: m2, Users: 2, Sim: sim}},
		m2:     {{ID: m1, Users: 2, Sim: sim}, {ID: series, Users: 2, Sim: sim}},
		series: {{ID: m2, Users: 2, Sim: sim}},
	}
	got := read(DefaultOptions())
	check := func(name string, got, want map[int][]reco.CoLiked) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for id, ns := range want {
			if len(got[id]) != len(ns) {
				t.Fatalf("%s: title %d has %v, want %v", name, id, got[id], ns)
			}
			for i, n := range ns {
				g := got[id][i]
				if g.ID != n.ID || g.Users != n.Users || math.Abs(g.Sim-n.Sim) > 1e-9 {
					t.Errorf("%s: title %d neighbour %d = %+v, want %+v", name, id, i, g, n)
				}
			}
		}
	}
	check("defaults", got, want)

	o := DefaultOptions()
	o.PerTitle = 1
	capped := map[int][]reco.CoLiked{m1: want[m1], m2: want[m2][:1], series: want[series]}
	check("one neighbour per title", read(o), capped)

	o = DefaultOptions()
	o.UserCap = 1
	check("one title per user", read(o), map[int][]reco.CoLiked{})

	o = DefaultOptions()
	o.MinUsers = 1
	if got := read(o); len(got[m3]) != 3 {
		t.Errorf("with one user enough, film3 should pair with film1, film2 and the show through u1, got %v", got[m3])
	}

	if got, err := repo.CoLiked(ctx, nil); err != nil || got != nil {
		t.Errorf("no ids: got %v, %v", got, err)
	}
}
