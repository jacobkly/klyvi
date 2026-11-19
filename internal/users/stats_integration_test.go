package users_test

import (
	"context"
	"testing"

	"klyvi-api/internal/users"
)

// DeleteAccountData must clear every app-side table for the user in one
// transaction. Seed a row in each, delete, and confirm they are gone.
func TestDeleteAccountData_RemovesAllAppRows(t *testing.T) {
	repo, dbConn, ctx := usernameTestRepo(t)
	id := newTestUser(t, repo, dbConn, ctx)

	// Seed one row in each app table. media_list/interactions need a
	// media_index spine row; 550 (Fight Club) is in the seeded catalog.
	var mediaID int
	if err := dbConn.GetContext(ctx, &mediaID, `
		insert into media_index (id, season_number, media_type)
		values (550, null, 'movie')
		on conflict (id, season_number, media_type) do update set media_type = excluded.media_type
		returning media_id
	`); err != nil {
		t.Fatalf("seed media_index: %v", err)
	}
	if _, err := dbConn.ExecContext(ctx, `
		insert into media_list (user_id, media_id, media_type) values ($1, $2, 'movie')
	`, id, mediaID); err != nil {
		t.Fatalf("seed media_list: %v", err)
	}
	if _, err := dbConn.ExecContext(ctx, `
		insert into interactions (user_id, media_id, media_type, kind) values ($1, $2, 'movie', 'logged')
	`, id, mediaID); err != nil {
		t.Fatalf("seed interactions: %v", err)
	}
	if _, err := dbConn.ExecContext(ctx, `
		insert into taste_profiles (user_id) values ($1)
	`, id); err != nil {
		t.Fatalf("seed taste_profiles: %v", err)
	}

	if err := repo.DeleteAccountData(ctx, id); err != nil {
		t.Fatalf("DeleteAccountData: %v", err)
	}

	for _, tbl := range []string{"media_list", "interactions", "taste_profiles"} {
		var n int
		if err := dbConn.GetContext(ctx, &n,
			`select count(*) from `+tbl+` where user_id = $1`, id); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("%s still has %d rows after delete", tbl, n)
		}
	}
	var users int
	if err := dbConn.GetContext(ctx, &users, `select count(*) from users where id = $1`, id); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 0 {
		t.Error("users row survived account deletion")
	}
}

// Proves the stats projections parse and scan against real Postgres — the
// aggregation itself is covered by the pure ComputeStats tests. Uses a
// fresh user, so both queries exercise their full column lists and return
// empty sets.
func TestStatsQueries_ScanAgainstRealSchema(t *testing.T) {
	repo, dbConn, ctx := usernameTestRepo(t)
	id := newTestUser(t, repo, dbConn, ctx)

	rows, err := repo.StatsRowsByUser(ctx, id)
	if err != nil {
		t.Fatalf("StatsRowsByUser: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("fresh user has %d stats rows, want 0", len(rows))
	}

	events, err := repo.StatsEventsByUser(ctx, id)
	if err != nil {
		t.Fatalf("StatsEventsByUser: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("fresh user has %d events, want 0", len(events))
	}

	// And the service end of it produces the fully-shaped zero payload.
	svc := users.NewService(repo)
	stats, err := svc.GetStats(context.Background(), id)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if len(stats.Activity) != 371 || len(stats.ScoreDistribution) != 10 {
		t.Errorf("zero payload malformed: activity=%d bands=%d", len(stats.Activity), len(stats.ScoreDistribution))
	}
}

// A tracked season's watch time comes from its own cached episodes, and
// its countries from the series' production_countries. The older columns
// these read (episode_run_time, origin_country) are never written by the
// TV cache, so every season counted zero minutes and no country.
func TestStatsRowsByUser_SeasonUsesEpisodeRuntimesAndSeriesCountries(t *testing.T) {
	repo, dbConn, ctx := usernameTestRepo(t)
	id := newTestUser(t, repo, dbConn, ctx)

	// A synthetic series far outside TMDB's id range, so it never collides
	// with the seeded catalog.
	const tvID = 990000101
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = dbConn.ExecContext(bg, `delete from media_list where user_id = $1`, id)
		_, _ = dbConn.ExecContext(bg, `delete from media_index where id = $1 and media_type = 'season'`, tvID)
		_, _ = dbConn.ExecContext(bg, `delete from tv_seasons where tv_id = $1`, tvID)
		_, _ = dbConn.ExecContext(bg, `delete from tv_series where tv_id = $1`, tvID)
	})

	if _, err := dbConn.ExecContext(ctx, `
		insert into tv_series (tv_id, name, genres, production_countries)
		values ($1, 'Stats Fixture', '[{"id":18,"name":"Drama"}]',
			'[{"iso_3166_1":"GB","name":"United Kingdom"}]')
	`, tvID); err != nil {
		t.Fatalf("seed tv_series: %v", err)
	}
	// Ten episodes: nine of 50 minutes and one TMDB has no runtime for,
	// which must not drag the average down.
	if _, err := dbConn.ExecContext(ctx, `
		insert into tv_seasons (tv_id, season_number, air_date, episodes)
		select $1, 1, date '2020-01-01', jsonb_agg(
			jsonb_build_object('episode_number', n, 'runtime', case when n = 10 then null else 50 end)
			order by n)
		from generate_series(1, 10) n
	`, tvID); err != nil {
		t.Fatalf("seed tv_seasons: %v", err)
	}
	var mediaID int
	if err := dbConn.GetContext(ctx, &mediaID, `
		insert into media_index (id, season_number, media_type) values ($1, 1, 'season')
		on conflict (id, season_number, media_type) do update set media_type = excluded.media_type
		returning media_id
	`, tvID); err != nil {
		t.Fatalf("seed media_index: %v", err)
	}
	if _, err := dbConn.ExecContext(ctx, `
		insert into media_list (user_id, media_id, media_type, status, episode_progress)
		values ($1, $2, 'season', 'completed', 10)
	`, id, mediaID); err != nil {
		t.Fatalf("seed media_list: %v", err)
	}

	rows, err := repo.StatsRowsByUser(ctx, id)
	if err != nil {
		t.Fatalf("StatsRowsByUser: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d stats rows, want 1", len(rows))
	}
	r := rows[0]
	if r.EpisodeCount == nil || *r.EpisodeCount != 10 {
		t.Errorf("episode count = %v, want 10", r.EpisodeCount)
	}
	if r.EpisodeRuntimeMinutes == nil || *r.EpisodeRuntimeMinutes != 50 {
		t.Errorf("episode runtime = %v, want 50 (the mean of the non-null runtimes)", r.EpisodeRuntimeMinutes)
	}
	if got := deref(r.EpisodeCount) * deref(r.EpisodeRuntimeMinutes); got != 500 {
		t.Errorf("season watch time = %d minutes, want 500", got)
	}
	if len(r.Countries) != 1 || r.Countries[0] != "GB" {
		t.Errorf("countries = %v, want [GB] from production_countries", r.Countries)
	}
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// An account delete unlinks the user's error reports: the error stays for
// the fix, the user id goes.
func TestDeleteAccountData_UnlinksErrorReports(t *testing.T) {
	repo, dbConn, ctx := usernameTestRepo(t)
	var exists bool
	if err := dbConn.GetContext(ctx, &exists, `select to_regclass('public.error_events') is not null`); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if !exists {
		t.Skip("migration 00043 not applied; error_events doesn't exist yet")
	}
	id := newTestUser(t, repo, dbConn, ctx)
	fingerprint := "test-unlink-" + id.String()
	if _, err := dbConn.ExecContext(ctx, `
		insert into error_events (fingerprint, source, kind, message, user_id)
		values ($1, 'web', 'Error', 'test', $2)`, fingerprint, id); err != nil {
		t.Fatalf("seed error_events: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbConn.ExecContext(context.Background(), `delete from error_events where fingerprint = $1`, fingerprint)
	})

	if err := repo.DeleteAccountData(ctx, id); err != nil {
		t.Fatalf("DeleteAccountData: %v", err)
	}

	var linked int
	if err := dbConn.GetContext(ctx, &linked, `select count(*) from error_events where user_id = $1`, id); err != nil {
		t.Fatalf("count: %v", err)
	}
	if linked != 0 {
		t.Errorf("%d error reports still name the deleted user", linked)
	}
}
