package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joeshaw/envdecode"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/timing"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/users"
)

// tier2FeedQueryBudget is the most DB round trips one Tier 2 feed request may
// make. Every query is a full homelab-to-Supabase round trip, so this number
// is the feed's latency floor.
const tier2FeedQueryBudget = 8

// TestTier2FeedQueryBudget builds the production recommender for a synthetic
// Tier 2 user and counts the queries one feed page costs.
func TestTier2FeedQueryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	userID := newTier2User(t, dbConn)

	orch := newRecoOrchestrator(dbConn, movies.NewRepository(dbConn), reco.DefaultConfig(), nil)

	// Warm the pool's connections and statement caches first, so the count
	// and timing below match a server that has been up for a while.
	if _, err := orch.Feed(context.Background(), userID, 30, 0); err != nil {
		t.Fatalf("warm-up Feed: %v", err)
	}

	ctx, rec := timing.NewContext(context.Background())
	page, err := orch.Feed(ctx, userID, 30, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(page) == 0 {
		t.Fatal("Feed returned no items; the synthetic user should get a Tier 2 feed")
	}

	snap := rec.Snapshot()
	t.Logf("tier 2 feed page 1: %d queries, %v in the DB", snap.DBQueries, snap.DB)
	if snap.DBQueries > tier2FeedQueryBudget {
		t.Fatalf("feed made %d queries, budget is %d", snap.DBQueries, tier2FeedQueryBudget)
	}
}

func openTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
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
	var cfg config.ConfDB
	if err := envdecode.StrictDecode(&cfg); err != nil {
		t.Skipf("DB env incomplete: %v", err)
	}
	conn, err := db.New(cfg)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// newTier2User creates a throwaway user who has rated 25 popular movies, enough
// signal for the orchestrator to pick Tier 2. Deleting the user cascades to
// its interactions and taste profile.
func newTier2User(t *testing.T, dbConn *sqlx.DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	id := uuid.New()
	t.Cleanup(func() {
		_, _ = dbConn.ExecContext(context.Background(), `delete from users where id = $1`, id)
	})
	if err := users.NewRepository(dbConn).EnsureUser(ctx, id); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}

	var mediaIDs []int
	if err := dbConn.SelectContext(ctx, &mediaIDs, `
		select mi.media_id
		from media_index mi
		join movies m on m.movie_id = mi.id
		where mi.media_type = 'movie' and m.keywords is not null and m.vote_count >= 1000
		order by m.popularity desc
		limit 25
	`); err != nil {
		t.Fatalf("pick movies: %v", err)
	}
	if len(mediaIDs) < 25 {
		t.Skipf("catalog has only %d rich movies; need 25", len(mediaIDs))
	}
	for _, mid := range mediaIDs {
		if _, err := dbConn.ExecContext(ctx, `
			insert into interactions (user_id, media_id, media_type, kind, rating, source)
			values ($1, $2, 'movie', 'rated', 85, 'onboarding')
		`, id, mid); err != nil {
			t.Fatalf("insert interaction: %v", err)
		}
	}
	return id
}
