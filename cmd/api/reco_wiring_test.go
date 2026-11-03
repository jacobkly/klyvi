package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joeshaw/envdecode"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/interactions"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/timing"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/tracking"
	"klyvi-api/internal/tv"
	"klyvi-api/internal/users"
)

// tier2FeedQueryBudget is the most DB round trips one Tier 2 feed request may
// make. Every query is a full homelab-to-Supabase round trip, so this number
// is the feed's latency floor. The fifth is the card details lookup, which
// replaces a /v1/movies/{id} request per card from the client.
const tier2FeedQueryBudget = 5

// TestTier2FeedQueryBudget builds the production recommender for a synthetic
// Tier 2 user and counts the queries one feed page costs.
func TestTier2FeedQueryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	userID := newTier2User(t, dbConn)

	orch := newRecoOrchestrator(dbConn, movies.NewRepository(dbConn), tv.NewRepository(dbConn), reco.DefaultConfig(), nil)
	// Cleanups run last-in first-out, so background profile saves land before
	// the user is deleted and the pool closes.
	t.Cleanup(orch.Wait)

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

	// The cards arrive complete, so the client never hydrates them.
	detailed := 0
	for _, s := range page {
		if s.Overview != "" && len(s.Genres) > 0 {
			detailed++
		}
	}
	if detailed == 0 {
		t.Error("no feed item carries an overview and genres")
	}
}

// TestCachedFeed_LaterPagesSkipTheDBUntilARating wires the feed cache the way
// main does. Page 2 must cost no queries, and a rating recorded through the
// interactions service must force a recompute that drops the rated title.
func TestCachedFeed_LaterPagesSkipTheDBUntilARating(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	userID := newTier2User(t, dbConn)

	feedCache := reco.NewFeedCache(time.Minute, 8)
	orch := newRecoOrchestrator(dbConn, movies.NewRepository(dbConn), tv.NewRepository(dbConn), reco.DefaultConfig(), nil).
		WithFeedCache(feedCache)
	t.Cleanup(orch.Wait)
	interactionsService := interactions.NewService(
		interactions.NewRepository(dbConn), tracking.NewRepository(dbConn)).
		WithFeedInvalidator(feedCache)

	first, err := orch.Feed(context.Background(), userID, 30, 0)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("page 1 is empty; the synthetic user should get a Tier 2 feed")
	}

	ctx, rec := timing.NewContext(context.Background())
	if _, err := orch.Feed(ctx, userID, 30, 30); err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if n := rec.Snapshot().DBQueries; n != 0 {
		t.Fatalf("page 2 made %d queries, want 0 from the cache", n)
	}

	rated := first[0]
	score := 90
	if _, err := interactionsService.Record(context.Background(), userID, interactions.RecordRequest{
		TMDBID: rated.TMDBID, MediaType: "movie", Kind: "rated", Rating: &score,
	}); err != nil {
		t.Fatalf("record rating: %v", err)
	}

	ctx, rec = timing.NewContext(context.Background())
	after, err := orch.Feed(ctx, userID, 30, 0)
	if err != nil {
		t.Fatalf("feed after rating: %v", err)
	}
	if rec.Snapshot().DBQueries == 0 {
		t.Fatal("feed after a rating was served from the cache")
	}
	for _, s := range after {
		if s.MediaID == rated.MediaID {
			t.Fatalf("%q was just rated but is still in the feed", rated.Title)
		}
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
