package tv_test

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// The 'tv' partition of the recommender's IDF inputs counts, per genre and
// keyword, how many voted series carry it. It never touches the movie rows.
func TestRefreshFeatureStats_CountsVotedSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const (
		genreID   = 2_100_000_901
		keywordID = 2_100_000_900
	)
	series := []struct {
		tvID      int
		voteCount int
		genres    string
		keywords  string
	}{
		{2_100_000_020, 10, `[{"id": 2100000901}]`, `[{"id": 2100000900}]`},
		{2_100_000_021, 5, `[]`, `[{"id": 2100000900}]`},
		// No votes: outside the eligible catalog, like movies.
		{2_100_000_022, 0, `[{"id": 2100000901}]`, `[{"id": 2100000900}]`},
	}
	cleanup := func() {
		for _, s := range series {
			cleanupExec(dbh, `delete from tv_series where tv_id = $1`, s.tvID)
		}
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		// Recount without the synthetic rows so they don't linger in the stats.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = repo.RefreshFeatureStats(ctx)
	})

	for _, s := range series {
		if _, err := dbh.ExecContext(ctx, `
			insert into tv_series (tv_id, vote_count, genres, keywords)
			values ($1, $2, $3::jsonb, $4::jsonb)`,
			s.tvID, s.voteCount, s.genres, s.keywords); err != nil {
			t.Fatalf("insert series %d: %v", s.tvID, err)
		}
	}

	movieSnapshot := func() (rows, corpus int) {
		t.Helper()
		if err := dbh.QueryRowContext(ctx, `
			select
				(select count(*) from feature_stats where media_type = 'movie'),
				coalesce((select doc_count from feature_corpus where media_type = 'movie'), 0)
		`).Scan(&rows, &corpus); err != nil {
			t.Fatalf("movie snapshot: %v", err)
		}
		return rows, corpus
	}
	movieRows, movieCorpus := movieSnapshot()

	if err := repo.RefreshFeatureStats(ctx); err != nil {
		t.Fatalf("RefreshFeatureStats: %v", err)
	}

	docCount := func(featureType string, id int) int {
		t.Helper()
		var n int
		if err := dbh.GetContext(ctx, &n, `
			select coalesce((
				select doc_count from feature_stats
				where media_type = 'tv' and feature_type = $1 and feature_id = $2
			), 0)`, featureType, id); err != nil {
			t.Fatalf("read %s %d: %v", featureType, id, err)
		}
		return n
	}
	if got := docCount("keyword", keywordID); got != 2 {
		t.Errorf("keyword doc_count = %d, want 2 (the unvoted series is excluded)", got)
	}
	if got := docCount("genre", genreID); got != 1 {
		t.Errorf("genre doc_count = %d, want 1", got)
	}

	var corpus, voted int
	if err := dbh.QueryRowContext(ctx, `
		select
			coalesce((select doc_count from feature_corpus where media_type = 'tv'), 0),
			(select count(*) from tv_series where vote_count > 0)
	`).Scan(&corpus, &voted); err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	if corpus != voted {
		t.Errorf("tv corpus = %d, want %d voted series", corpus, voted)
	}

	if rows, c := movieSnapshot(); rows != movieRows || c != movieCorpus {
		t.Errorf("movie stats changed: %d rows / corpus %d, was %d / %d", rows, c, movieRows, movieCorpus)
	}
}

// A series' crew for the recommender is its creators plus its crew in the
// given jobs. A series with neither is left out.
func TestRecoCrew_AddsCreators(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const withCrew, bare = 2_100_000_030, 2_100_000_031
	cleanup := func() {
		cleanupExec(dbh, `delete from tv_series where tv_id = any($1)`, []int{withCrew, bare})
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := dbh.ExecContext(ctx, `
		insert into tv_series (tv_id, created_by, credits) values
		($1, '[{"id": 2100000905, "name": "Creator"}]'::jsonb,
		     '{"cast": [], "crew": [{"id": 2100000906, "job": "Original Music Composer"},
		                            {"id": 2100000907, "job": "Producer"},
		                            {"id": 2100000905, "job": "Writer"}]}'::jsonb),
		($2, '[]'::jsonb, '{"cast": [], "crew": [{"id": 2100000907, "job": "Producer"}]}'::jsonb)`,
		withCrew, bare); err != nil {
		t.Fatalf("insert series: %v", err)
	}

	got, err := repo.RecoCrew(ctx, []string{"Writer", "Original Music Composer"})
	if err != nil {
		t.Fatalf("RecoCrew: %v", err)
	}
	if want := []int{2100000905, 2100000906}; !reflect.DeepEqual(got[withCrew], want) {
		t.Errorf("crew = %v, want %v", got[withCrew], want)
	}
	if ids, ok := got[bare]; ok {
		t.Errorf("a series with only producers has crew %v, want it left out", ids)
	}
}

// Series keyword sets skip series nobody voted on and series with one keyword.
func TestRecoKeywordSets(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const voted, unvoted, single = 2_100_000_032, 2_100_000_033, 2_100_000_034
	cleanup := func() {
		cleanupExec(dbh, `delete from tv_series where tv_id = any($1)`, []int{voted, unvoted, single})
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := dbh.ExecContext(ctx, `
		insert into tv_series (tv_id, vote_count, keywords) values
		($1, 10, '[{"id": 2100000921}, {"id": 2100000922}]'),
		($2, 0, '[{"id": 2100000921}, {"id": 2100000923}]'),
		($3, 10, '[{"id": 2100000924}]')`, voted, unvoted, single); err != nil {
		t.Fatalf("insert series: %v", err)
	}

	sets, err := repo.RecoKeywordSets(ctx)
	if err != nil {
		t.Fatalf("RecoKeywordSets: %v", err)
	}
	var found []int
	for _, set := range sets {
		for _, k := range set {
			if k >= 2100000921 && k <= 2100000924 {
				found = append(found, k)
			}
		}
	}
	if want := []int{2100000921, 2100000922}; !reflect.DeepEqual(found, want) {
		t.Errorf("test keywords read = %v, want only the voted series' %v", found, want)
	}
}

// Series cast keeps the first top billed and leaves out series with none.
func TestRecoCast_KeepsTheTopBilled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const withCast, bare = 2_100_000_035, 2_100_000_036
	cleanup := func() {
		cleanupExec(dbh, `delete from tv_series where tv_id = any($1)`, []int{withCast, bare})
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := dbh.ExecContext(ctx, `
		insert into tv_series (tv_id, credits) values
		($1, '{"crew": [], "cast": [{"id": 2100000942, "order": 1}, {"id": 2100000941, "order": 0},
		                            {"id": 2100000943, "order": 2}]}'::jsonb),
		($2, '{"crew": [], "cast": []}'::jsonb)`,
		withCast, bare); err != nil {
		t.Fatalf("insert series: %v", err)
	}

	got, err := repo.RecoCast(ctx, 2)
	if err != nil {
		t.Fatalf("RecoCast: %v", err)
	}
	if want := []int{2100000941, 2100000942}; !reflect.DeepEqual(got[withCast], want) {
		t.Errorf("cast = %v, want %v", got[withCast], want)
	}
	if ids, ok := got[bare]; ok {
		t.Errorf("a series with no cast has cast %v, want it left out", ids)
	}
}
