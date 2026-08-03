package users_test

import (
	"context"
	"testing"

	"klyvi-api/internal/users"
)

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
