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
