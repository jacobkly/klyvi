package movies_test

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tracking"
	"klyvi-api/internal/users"
)

// The refresh tests backdate their rows to the 1990s and look for stale rows
// before 2000. Real rows are younger than that, so the tests only ever see
// their own.
var (
	staleCutoff = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	in1990      = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	in1991      = time.Date(1991, 1, 1, 0, 0, 0, 0, time.UTC)
)

func backdateMovie(t *testing.T, dbConn *sqlx.DB, id int, at *time.Time) {
	t.Helper()
	if _, err := dbConn.ExecContext(context.Background(),
		`update movies set updated_at = $2 where movie_id = $1`, id, at); err != nil {
		t.Fatalf("backdate movie %d: %v", id, err)
	}
}

// only keeps the rows whose id is one of ids, in order.
func only(rows []movies.StaleRow, ids ...int) []int {
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var got []int
	for _, r := range rows {
		if want[r.ID] {
			got = append(got, r.ID)
		}
	}
	return got
}

func TestListStaleMovies_OldestFirstBeforeTheCutoff(t *testing.T) {
	repo, dbConn := openTestRepo(t)
	ctx := context.Background()
	base := 2_000_000_000 + rand.Intn(100_000_000)
	never, oldest, older, fresh := base, base+1, base+2, base+3
	cleanupTestMovies(t, dbConn, never, oldest, older, fresh)

	for _, id := range []int{never, oldest, older, fresh} {
		if err := repo.InsertMovie(ctx, testMovie(id, true)); err != nil {
			t.Fatalf("insert %d: %v", id, err)
		}
	}
	backdateMovie(t, dbConn, never, nil)
	backdateMovie(t, dbConn, oldest, &in1990)
	backdateMovie(t, dbConn, older, &in1991)

	rows, err := repo.ListStaleMovies(ctx, staleCutoff, 1000)
	if err != nil {
		t.Fatalf("ListStaleMovies: %v", err)
	}
	got := only(rows, never, oldest, older, fresh)
	want := []int{never, oldest, older}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("stale ids = %v, want %v (never stamped, then oldest first, fresh left out)", got, want)
	}

	rows, err = repo.ListStaleMovies(ctx, staleCutoff, 1)
	if err != nil {
		t.Fatalf("ListStaleMovies limit 1: %v", err)
	}
	if len(rows) != 1 || rows[0].UpdatedAt.After(in1990) {
		t.Errorf("limit 1 gave %+v, want one row no younger than 1990", rows)
	}
}

func TestReplaceMovie_OverwritesAFullRowAndStampsIt(t *testing.T) {
	repo, dbConn := openTestRepo(t)
	ctx := context.Background()
	id := 2_000_000_000 + rand.Intn(100_000_000)
	cleanupTestMovies(t, dbConn, id)

	if err := repo.InsertMovie(ctx, testMovie(id, true)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	backdateMovie(t, dbConn, id, &in1990)

	renamed := testMovie(id, true)
	title := "Klyvi test movie, retitled on TMDB"
	renamed.Title = &title
	if err := repo.ReplaceMovie(ctx, renamed); err != nil {
		t.Fatalf("ReplaceMovie: %v", err)
	}

	var gotTitle string
	var updatedAt time.Time
	if err := dbConn.QueryRowContext(ctx,
		`select title, updated_at from movies where movie_id = $1`, id).Scan(&gotTitle, &updatedAt); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if gotTitle != title {
		t.Errorf("title = %q, want %q", gotTitle, title)
	}
	if time.Since(updatedAt) > time.Hour {
		t.Errorf("updated_at = %v, want it stamped now", updatedAt)
	}

	rows, err := repo.ListStaleMovies(ctx, staleCutoff, 1000)
	if err != nil {
		t.Fatalf("ListStaleMovies: %v", err)
	}
	if got := only(rows, id); len(got) != 0 {
		t.Error("a replaced movie is still listed as stale")
	}
}

// TMDB removed the title. The cached row goes, but the user's tracking of it
// stays, because it hangs off media_index, not movies.
func TestDeleteMovie_KeepsTheTrackingOfIt(t *testing.T) {
	repo, dbConn := openTestRepo(t)
	ctx := context.Background()
	id := 2_000_000_000 + rand.Intn(100_000_000)
	cleanupTestMovies(t, dbConn, id)

	if err := repo.InsertMovie(ctx, testMovie(id, true)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	userID := uuid.New()
	if err := users.NewRepository(dbConn).EnsureUser(ctx, userID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbConn.ExecContext(context.Background(), `delete from media_list where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(context.Background(), `delete from users where id = $1`, userID)
	})
	track := tracking.NewService(tracking.NewRepository(dbConn))
	if _, err := track.Add(ctx, userID, tracking.AddRequest{TMDBID: id, MediaType: tracking.MediaTypeMovie}); err != nil {
		t.Fatalf("track: %v", err)
	}

	if err := repo.DeleteMovie(ctx, id); err != nil {
		t.Fatalf("DeleteMovie: %v", err)
	}

	cached, err := repo.GetByTMDBID(ctx, id)
	if err != nil {
		t.Fatalf("GetByTMDBID: %v", err)
	}
	if cached != nil {
		t.Error("the movie is still cached")
	}
	entries, err := track.List(ctx, userID, tracking.ListFilters{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("tracking entries = %d, want 1", len(entries))
	}
}

func testCollection(collectionID int, movieIDs ...int) []movies.MovieCollection {
	out := make([]movies.MovieCollection, len(movieIDs))
	for i, id := range movieIDs {
		pos := i + 1
		out[i] = movies.MovieCollection{CollectionID: collectionID, MovieID: id, Name: "Klyvi test part", Position: &pos}
	}
	return out
}

func cleanupTestCollections(t *testing.T, dbConn *sqlx.DB, ids ...int) {
	t.Helper()
	clean := func() {
		_, _ = dbConn.ExecContext(context.Background(), `delete from movie_collections where collection_id = any($1)`, ids)
	}
	clean()
	t.Cleanup(clean)
}

func collectionParts(t *testing.T, dbConn *sqlx.DB, collectionID int) []int {
	t.Helper()
	var ids []int
	if err := dbConn.SelectContext(context.Background(), &ids,
		`select movie_id from movie_collections where collection_id = $1 order by position`, collectionID); err != nil {
		t.Fatalf("read collection %d: %v", collectionID, err)
	}
	return ids
}

// A collection is as old as its oldest part. Replacing it swaps in TMDB's
// current parts, drops the ones TMDB no longer lists, and makes it fresh.
func TestCollections_StaleReplaceAndDelete(t *testing.T) {
	repo, dbConn := openTestRepo(t)
	ctx := context.Background()
	base := 2_000_000_000 + rand.Intn(100_000_000)
	older, old, fresh := base, base+1, base+2
	cleanupTestCollections(t, dbConn, older, old, fresh)

	for _, c := range [][]movies.MovieCollection{
		testCollection(older, 1, 2),
		testCollection(old, 3, 4),
		testCollection(fresh, 5),
	} {
		if err := repo.InsertMovieCollectionBatch(ctx, c); err != nil {
			t.Fatalf("insert collection: %v", err)
		}
	}
	for _, s := range []struct {
		id, movie int
		at        time.Time
	}{{older, 2, in1990}, {old, 3, in1991}, {old, 4, in1991}} {
		if _, err := dbConn.ExecContext(ctx,
			`update movie_collections set created_at = $3 where collection_id = $1 and movie_id = $2`,
			s.id, s.movie, s.at); err != nil {
			t.Fatalf("backdate: %v", err)
		}
	}

	rows, err := repo.ListStaleCollections(ctx, staleCutoff, 1000)
	if err != nil {
		t.Fatalf("ListStaleCollections: %v", err)
	}
	if got := only(rows, older, old, fresh); len(got) != 2 || got[0] != older || got[1] != old {
		t.Errorf("stale collections = %v, want [%d %d]", got, older, old)
	}

	if err := repo.ReplaceCollection(ctx, older, testCollection(older, 2, 6)); err != nil {
		t.Fatalf("ReplaceCollection: %v", err)
	}
	if got := collectionParts(t, dbConn, older); len(got) != 2 || got[0] != 2 || got[1] != 6 {
		t.Errorf("parts after replace = %v, want [2 6]", got)
	}
	if err := repo.DeleteCollection(ctx, old); err != nil {
		t.Fatalf("DeleteCollection: %v", err)
	}
	if got := collectionParts(t, dbConn, old); len(got) != 0 {
		t.Errorf("parts after delete = %v, want none", got)
	}

	rows, err = repo.ListStaleCollections(ctx, staleCutoff, 1000)
	if err != nil {
		t.Fatalf("ListStaleCollections: %v", err)
	}
	if got := only(rows, older, old, fresh); len(got) != 0 {
		t.Errorf("stale collections after the refresh = %v, want none", got)
	}
}

func TestRefreshMovie(t *testing.T) {
	tests := []struct {
		name        string
		tmdbErr     error
		wantRemoved bool
		wantErr     bool
		wantReplace int
		wantDelete  int
	}{
		{name: "TMDB has it: the row is replaced", wantReplace: 1},
		{name: "TMDB removed it: the row is deleted", tmdbErr: &tmdb.StatusError{Status: http.StatusNotFound}, wantRemoved: true, wantDelete: 1},
		{name: "TMDB is throttling: nothing is written", tmdbErr: &tmdb.StatusError{Status: http.StatusTooManyRequests}, wantErr: true},
		{name: "TMDB is down: nothing is written", tmdbErr: errors.New("connection reset"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeMovieRepo{}
			svc := movies.NewService(&fakeTMDB{resp: fullDetail(550), err: tt.tmdbErr}, repo)

			removed, err := svc.RefreshMovie(context.Background(), 550)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if removed != tt.wantRemoved {
				t.Errorf("removed = %v, want %v", removed, tt.wantRemoved)
			}
			if len(repo.replaced) != tt.wantReplace || len(repo.deleted) != tt.wantDelete {
				t.Errorf("replaced %d, deleted %d; want %d and %d", len(repo.replaced), len(repo.deleted), tt.wantReplace, tt.wantDelete)
			}
			if tt.wantReplace == 1 && repo.replaced[0].Keywords == nil {
				t.Error("the replacement lost its keywords")
			}
			if tt.tmdbErr != nil && tt.wantErr && !errors.Is(err, tt.tmdbErr) {
				t.Errorf("err = %v, want it to wrap %v", err, tt.tmdbErr)
			}
		})
	}
}

func TestRefreshCollection(t *testing.T) {
	parts := map[string]any{"parts": []any{
		map[string]any{"id": float64(1891), "title": "The Empire Strikes Back"},
		map[string]any{"id": float64(11), "title": "Star Wars"},
	}}
	tests := []struct {
		name        string
		tmdbErr     error
		wantRemoved bool
		wantErr     bool
		wantParts   int
		wantDelete  int
	}{
		{name: "TMDB has it: the parts are replaced", wantParts: 2},
		{name: "TMDB removed it: the collection is deleted", tmdbErr: &tmdb.StatusError{Status: http.StatusNotFound}, wantRemoved: true, wantDelete: 1},
		{name: "TMDB fails: nothing is written", tmdbErr: errors.New("connection reset"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeMovieRepo{}
			svc := movies.NewService(&fakeTMDB{resp: parts, err: tt.tmdbErr}, repo)

			removed, err := svc.RefreshCollection(context.Background(), 10)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if removed != tt.wantRemoved {
				t.Errorf("removed = %v, want %v", removed, tt.wantRemoved)
			}
			if len(repo.replacedCollection) != tt.wantParts || len(repo.deletedCollections) != tt.wantDelete {
				t.Errorf("replaced %d parts, deleted %d; want %d and %d",
					len(repo.replacedCollection), len(repo.deletedCollections), tt.wantParts, tt.wantDelete)
			}
		})
	}
}
