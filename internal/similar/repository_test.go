package similar_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/similar"
)

// Synthetic titles and keywords sit far above TMDB's ids, so the tests never
// match or touch a real row.
const base = 2_100_000_900

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
	return dbConn
}

func requireMigration(t *testing.T, dbh *sqlx.DB, version int64) {
	t.Helper()
	var current int64
	if err := dbh.Get(&current, `select coalesce(max(version_id), 0) from goose_db_version where is_applied`); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if current < version {
		t.Skipf("migration %05d not applied (DB is at %d)", version, current)
	}
}

func exec(t *testing.T, dbh *sqlx.DB, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := dbh.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", strings.Fields(query)[0], err)
	}
}

// features renders {id, name} pairs as the JSONB the cache stores, naming
// each id "k<offset>".
func features(ids ...int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf(`{"id": %d, "name": "k%d"}`, id, id-base)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

type seedTitle struct {
	offset     int
	votes      int
	keywords   []int // offsets from base
	collection int   // 0 for none
	indexed    bool
}

func seedMovies(t *testing.T, dbh *sqlx.DB, titles []seedTitle) {
	t.Helper()
	clean := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = dbh.ExecContext(ctx, `delete from media_index where media_type = 'movie' and id between $1 and $1 + 99`, base)
		_, _ = dbh.ExecContext(ctx, `delete from movies where movie_id between $1 and $1 + 99`, base)
	}
	clean()
	t.Cleanup(clean)

	for _, s := range titles {
		ids := make([]int, len(s.keywords))
		for i, k := range s.keywords {
			ids[i] = base + k
		}
		var coll any
		if s.collection != 0 {
			coll = fmt.Sprintf(`{"id": %d, "name": "c"}`, base+s.collection)
		}
		exec(t, dbh, `
			insert into movies (movie_id, title, vote_count, release_date, poster_path, genres, keywords, belongs_to_collection)
			values ($1, $2, $3, '2011-02-03', '/p.jpg', '[{"id": 18, "name": "Drama"}]'::jsonb, $4::jsonb, $5::jsonb)`,
			base+s.offset, fmt.Sprintf("movie %d", s.offset), s.votes, features(ids...), coll)
		if s.indexed {
			exec(t, dbh, `insert into media_index (id, media_type) values ($1, 'movie')`, base+s.offset)
		}
	}
}

func offsets(cands []similar.Candidate) []int {
	out := make([]int, len(cands))
	for i, c := range cands {
		out[i] = c.TMDBID - base
	}
	return out
}

func TestMovieCatalog(t *testing.T) {
	dbh := setupDB(t)
	seedMovies(t, dbh, []seedTitle{
		{offset: 0, votes: 500, keywords: []int{50, 51, 52, 53}, collection: 90, indexed: true}, // the source
		{offset: 1, votes: 100, keywords: []int{50}, indexed: true},                             // one shared, few votes
		{offset: 2, votes: 900, keywords: []int{50}, indexed: true},                             // one shared, many votes
		{offset: 3, votes: 30, keywords: []int{50, 51, 52, 60}, collection: 90, indexed: true},  // three shared
		{offset: 4, votes: 10, keywords: []int{50, 51, 52, 53}, indexed: true},                  // under the vote floor
		{offset: 5, votes: 800, keywords: []int{50, 51}, indexed: false},                        // not indexed
		{offset: 6, votes: 800, keywords: []int{61}, indexed: true},                             // nothing shared
	})
	ctx := context.Background()
	cat := similar.NewMovieCatalog(dbh)

	src, err := cat.Source(ctx, base)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if src == nil {
		t.Fatal("Source returned nil for a cached movie")
	}
	if len(src.KeywordIDs) != 4 || src.CollectionID != base+90 {
		t.Errorf("source keywords %v collection %d, want 4 keywords and collection %d", src.KeywordIDs, src.CollectionID, base+90)
	}
	if src.KeywordNames[base+51] != "k51" || src.GenreNames[18] != "Drama" {
		t.Errorf("source names = %v / %v, want the cached names", src.KeywordNames, src.GenreNames)
	}

	missing, err := cat.Source(ctx, base+99)
	if err != nil || missing != nil {
		t.Errorf("Source for an uncached id = %v, %v; want nil, nil", missing, err)
	}

	t.Run("candidates share a keyword, clear the floor and are indexed, best overlap first", func(t *testing.T) {
		got, err := cat.Candidates(ctx, src, 25, 10)
		if err != nil {
			t.Fatalf("Candidates: %v", err)
		}
		if want := []int{3, 2, 1}; !sameInts(offsets(got), want) {
			t.Fatalf("candidates = %v, want %v", offsets(got), want)
		}
		top := got[0]
		if top.MediaID == 0 || top.Year != 2011 || top.Title != "movie 3" || top.CollectionID != base+90 || len(top.KeywordIDs) != 4 {
			t.Errorf("top candidate = %+v, want its row's fields", top)
		}
	})

	t.Run("limit bounds the pool", func(t *testing.T) {
		got, err := cat.Candidates(ctx, src, 25, 1)
		if err != nil {
			t.Fatalf("Candidates: %v", err)
		}
		if !sameInts(offsets(got), []int{3}) {
			t.Errorf("candidates = %v, want only the best", offsets(got))
		}
	})

	t.Run("resolve returns indexed titles with their collection", func(t *testing.T) {
		got, err := cat.Resolve(ctx, []int{base + 3, base + 5, base + 98})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(got) != 1 || got[base+3].MediaID == 0 || got[base+3].CollectionID != base+90 {
			t.Errorf("resolved = %+v, want only the indexed title with its collection", got)
		}
	})

	t.Run("by id returns indexed titles over the floor, keywords shared or not", func(t *testing.T) {
		got, err := cat.ByTMDBIDs(ctx, []int{base + 6, base + 4, base + 5, base + 3, base + 98}, 25)
		if err != nil {
			t.Fatalf("ByTMDBIDs: %v", err)
		}
		if !sameInts(offsets(got), []int{3, 6}) {
			t.Fatalf("by id = %v, want [3 6]", offsets(got))
		}
		if got[0].MediaID == 0 || got[0].Title != "movie 3" || got[0].CollectionID != base+90 {
			t.Errorf("by id = %+v, want its row's fields", got[0])
		}
		if none, err := cat.ByTMDBIDs(ctx, nil, 25); err != nil || none == nil || len(none) != 0 {
			t.Errorf("no ids = %v, %v; want an empty list", none, err)
		}
	})
}

func TestTVCatalog(t *testing.T) {
	dbh := setupDB(t)
	requireMigration(t, dbh, 30)
	clean := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = dbh.ExecContext(ctx, `delete from media_index where media_type = 'tv' and id between $1 and $1 + 99`, base)
		_, _ = dbh.ExecContext(ctx, `delete from tv_series where tv_id between $1 and $1 + 99`, base)
	}
	clean()
	t.Cleanup(clean)

	// The source holds TMDB's {"results": [...]} envelope, as rows cached
	// before the normalizer unwrapped it do.
	exec(t, dbh, `
		insert into tv_series (tv_id, name, vote_count, first_air_date, genres, keywords)
		values ($1, 'source', 500, '2008-01-20', '[{"id": 18, "name": "Drama"}]'::jsonb, $2::jsonb)`,
		base, `{"results": `+features(base+50, base+51)+`}`)
	exec(t, dbh, `
		insert into tv_series (tv_id, name, vote_count, first_air_date, genres, keywords)
		values ($1, 'match', 100, '2013-09-22', '[]'::jsonb, $2::jsonb)`,
		base+1, features(base+51))
	exec(t, dbh, `insert into media_index (id, media_type) values ($1, 'tv'), ($2, 'tv')`, base, base+1)

	ctx := context.Background()
	cat := similar.NewTVCatalog(dbh)
	src, err := cat.Source(ctx, base)
	if err != nil || src == nil {
		t.Fatalf("Source = %v, %v", src, err)
	}
	if len(src.KeywordIDs) != 2 || src.KeywordNames[base+50] != "k50" {
		t.Errorf("source = %+v, want both keywords named from the envelope", src)
	}

	got, err := cat.Candidates(ctx, src, 25, 10)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 1 || got[0].TMDBID != base+1 || got[0].Title != "match" || got[0].Year != 2013 || got[0].MediaID == 0 {
		t.Errorf("candidates = %+v, want the one matching series", got)
	}

	resolved, err := cat.Resolve(ctx, []int{base + 1, base + 98})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved) != 1 || resolved[base+1].MediaID == 0 {
		t.Errorf("resolved = %+v, want the indexed series", resolved)
	}

	byID, err := cat.ByTMDBIDs(ctx, []int{base, base + 1, base + 98}, 25)
	if err != nil {
		t.Fatalf("ByTMDBIDs: %v", err)
	}
	if len(byID) != 2 || byID[1].Title != "match" || byID[1].MediaID == 0 {
		t.Errorf("by id = %+v, want both indexed series", byID)
	}
}
