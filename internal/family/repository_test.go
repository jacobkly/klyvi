package family_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/family"
	"klyvi-api/internal/platform/db"
)

// Synthetic titles and Wikidata items sit far above any real id, so the tests
// never match or touch a real row.
const base = 2_100_002_000

func setupDB(t *testing.T) *sqlx.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env.dev")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip(".env.dev not found; skipping DB-dependent test")
		}
		dir = parent
	}
	if err := godotenv.Load(filepath.Join(dir, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}
	dbConn, err := db.New(*config.NewDB())
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	t.Cleanup(func() { _ = dbConn.Close() })

	var version int64
	if err := dbConn.Get(&version, `select coalesce(max(version_id), 0) from goose_db_version where is_applied`); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if version < 40 {
		t.Skipf("migration 00040 not applied (DB is at %d)", version)
	}
	return dbConn
}

func exec(t *testing.T, dbh *sqlx.DB, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := dbh.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", strings.Fields(query)[0], err)
	}
}

// title is one synthetic catalog title and the Wikidata series and
// franchises it belongs to (offsets from base).
type title struct {
	mediaType  string
	offset     int
	date       string // "" for none
	series     []int
	franchises []int
	cached     bool // in the movies or tv_series cache
}

func seed(t *testing.T, dbh *sqlx.DB, titles []title) {
	t.Helper()
	clean := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = dbh.ExecContext(ctx, `delete from wikidata_facts where tmdb_id between $1 and $1 + 99`, base)
		_, _ = dbh.ExecContext(ctx, `delete from movies where movie_id between $1 and $1 + 99`, base)
		_, _ = dbh.ExecContext(ctx, `delete from tv_series where tv_id between $1 and $1 + 99`, base)
	}
	clean()
	t.Cleanup(clean)

	ids := func(offs []int) []int {
		out := make([]int, len(offs))
		for i, o := range offs {
			out[i] = base + o
		}
		return out
	}
	for _, s := range titles {
		var date any
		if s.date != "" {
			date = s.date
		}
		exec(t, dbh, `
			insert into wikidata_facts (media_type, tmdb_id, wikidata_id, series, franchises)
			values ($1, $2, $2, $3, $4)`,
			s.mediaType, base+s.offset, ids(s.series), ids(s.franchises))
		if !s.cached {
			continue
		}
		if s.mediaType == "movie" {
			exec(t, dbh, `insert into movies (movie_id, title, release_date, poster_path) values ($1, $2, $3, '/m.jpg')`,
				base+s.offset, "film "+string(rune('a'+s.offset)), date)
		} else {
			exec(t, dbh, `insert into tv_series (tv_id, name, first_air_date, poster_path) values ($1, $2, $3, '/t.jpg')`,
				base+s.offset, "show "+string(rune('a'+s.offset)), date)
		}
	}
}

func keys(titles []family.Title) []string {
	out := make([]string, len(titles))
	for i, f := range titles {
		out[i] = f.MediaType + ":" + string(rune('a'+f.TMDBID-base))
	}
	return out
}

func TestFamily_SharedSeriesOrFranchiseInReleaseOrder(t *testing.T) {
	dbh := setupDB(t)
	// a: the show. b: a film in its franchise. c: a show in its series.
	// d: a film with nothing shared. e: in the franchise but not cached.
	seed(t, dbh, []title{
		{mediaType: "tv", offset: 0, date: "2008-01-20", series: []int{90}, franchises: []int{91}, cached: true},
		{mediaType: "movie", offset: 1, date: "2019-10-11", franchises: []int{91}, cached: true},
		{mediaType: "tv", offset: 2, date: "2015-02-08", series: []int{90}, cached: true},
		{mediaType: "movie", offset: 3, date: "2001-01-01", franchises: []int{92}, cached: true},
		{mediaType: "movie", offset: 4, date: "2010-01-01", franchises: []int{91}, cached: false},
	})
	repo := family.NewRepository(dbh)
	ctx := context.Background()

	got, err := repo.Family(ctx, "tv", base)
	if err != nil {
		t.Fatalf("Family: %v", err)
	}
	if want := []string{"tv:c", "movie:b"}; !slices.Equal(keys(got), want) {
		t.Errorf("show's family = %v, want %v (release order, itself and the uncached left out)", keys(got), want)
	}
	if len(got) > 0 && (got[0].Title != "show c" || got[0].PosterPath == nil || got[0].ReleaseDate == nil) {
		t.Errorf("first title = %+v, want its name, poster and date from the cache", got[0])
	}

	// The film finds the show back through the franchise, but not the show
	// that only shares the series.
	got, err = repo.Family(ctx, "movie", base+1)
	if err != nil {
		t.Fatalf("Family: %v", err)
	}
	if want := []string{"tv:a"}; !slices.Equal(keys(got), want) {
		t.Errorf("film's family = %v, want %v", keys(got), want)
	}
}

func TestFamily_NoneIsEmpty(t *testing.T) {
	dbh := setupDB(t)
	seed(t, dbh, []title{
		{mediaType: "movie", offset: 3, date: "2001-01-01", franchises: []int{92}, cached: true},
	})
	repo := family.NewRepository(dbh)
	for _, id := range []int{base + 3, base + 50} {
		got, err := repo.Family(context.Background(), "movie", id)
		if err != nil {
			t.Fatalf("Family(%d): %v", id, err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("Family(%d) = %#v, want an empty, non-nil list", id, got)
		}
	}
}
