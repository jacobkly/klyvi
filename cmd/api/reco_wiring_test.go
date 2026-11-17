package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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
// make on the database catalog (RECO_CATALOG_REFRESH=0). Every query is a full
// homelab-to-Supabase round trip. The fifth is the card details lookup, which
// replaces a /v1/movies/{id} request per card from the client. The sixth and
// seventh are the series draw and the series cards, which run beside the film
// draw and the film cards, so they add round trips but not a step to the
// feed's latency floor.
const tier2FeedQueryBudget = 7

// tier2MemFeedQueryBudget is the same budget on the in-memory catalog, the
// default. The history and the taste draws come from memory, which leaves the
// user's own reads and the two card lookups.
const tier2MemFeedQueryBudget = 4

// openMemCatalog loads the in-memory catalog from the test database the way
// main does at startup.
func openMemCatalog(t *testing.T, dbConn *sqlx.DB) *cachedCatalog {
	t.Helper()
	movieRepo, tvRepo := movies.NewRepository(dbConn), tv.NewRepository(dbConn)
	c := newCachedCatalog(newCatalogAdapter(movieRepo, tvRepo), repoCatalogLoader(movieRepo, tvRepo))
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return c
}

// TestTier2FeedQueryBudget builds the production recommender for a synthetic
// Tier 2 user and counts the queries one feed page costs, on each catalog.
func TestTier2FeedQueryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	userID := newTier2User(t, dbConn)

	tests := []struct {
		name    string
		catalog func() reco.CatalogRepository
		budget  int
	}{
		{"database", func() reco.CatalogRepository {
			return newCatalogAdapter(movies.NewRepository(dbConn), tv.NewRepository(dbConn))
		}, tier2FeedQueryBudget},
		{"in memory", func() reco.CatalogRepository { return openMemCatalog(t, dbConn) }, tier2MemFeedQueryBudget},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orch := newRecoOrchestrator(dbConn, tc.catalog(), reco.DefaultConfig(), nil, recoSides{})
			// Cleanups run last-in first-out, so background profile saves land
			// before the user is deleted and the pool closes.
			t.Cleanup(orch.Wait)

			// Warm the pool's connections and statement caches first, so the
			// count and timing below match a server that has been up a while.
			if _, err := orch.Feed(context.Background(), userID, reco.FeedAll, 30, 0); err != nil {
				t.Fatalf("warm-up Feed: %v", err)
			}

			ctx, rec := timing.NewContext(context.Background())
			page, err := orch.Feed(ctx, userID, reco.FeedAll, 30, 0)
			if err != nil {
				t.Fatalf("Feed: %v", err)
			}
			if len(page) == 0 {
				t.Fatal("Feed returned no items; the synthetic user should get a Tier 2 feed")
			}

			snap := rec.Snapshot()
			t.Logf("tier 2 feed page 1: %d queries, %v in the DB", snap.DBQueries, snap.DB)
			if snap.DBQueries > tc.budget {
				t.Fatalf("feed made %d queries, budget is %d", snap.DBQueries, tc.budget)
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
		})
	}
}

// The in-memory draws must return what the SQL draws return, in the same
// order, so moving the feed onto memory changes its speed and not its picks.
// The taste comes from real catalog titles, at the pool's real sizes.
func TestCachedCatalog_MatchesTheDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	ctx := context.Background()
	sqlCat := newCatalogAdapter(movies.NewRepository(dbConn), tv.NewRepository(dbConn))
	mem := openMemCatalog(t, dbConn)
	if films, series := mem.Size(); films == 0 || series == 0 {
		t.Skipf("catalog holds %d films and %d series; need some of each", films, series)
	}

	var filmTaste, seriesTaste struct {
		Genres   db.IntArray `db:"genre_ids"`
		Keywords db.IntArray `db:"keyword_ids"`
	}
	if err := dbConn.GetContext(ctx, &filmTaste, `
		select genre_ids, keyword_ids from movies
		where cardinality(keyword_ids) >= 5 order by vote_count desc, movie_id limit 1
	`); err != nil {
		t.Skipf("no film with keywords: %v", err)
	}
	if err := dbConn.GetContext(ctx, &seriesTaste, `
		select genre_ids, keyword_ids from tv_series
		where cardinality(keyword_ids) >= 5 order by vote_count desc, tv_id limit 1
	`); err != nil {
		t.Skipf("no series with keywords: %v", err)
	}

	type draw func(context.Context, reco.CatalogRepository) ([]reco.Candidate, error)
	draws := map[string]draw{
		"film taste": func(ctx context.Context, c reco.CatalogRepository) ([]reco.Candidate, error) {
			return c.CandidatesByTaste(ctx, filmTaste.Genres, filmTaste.Keywords, 25, 720)
		},
		"film genres only": func(ctx context.Context, c reco.CatalogRepository) ([]reco.Candidate, error) {
			return c.CandidatesByTaste(ctx, filmTaste.Genres, nil, 25, 300)
		},
		"series taste": func(ctx context.Context, c reco.CatalogRepository) ([]reco.Candidate, error) {
			return c.SeriesCandidatesByTaste(ctx, seriesTaste.Genres, seriesTaste.Keywords, 25, 480)
		},
		"popular films": func(ctx context.Context, c reco.CatalogRepository) ([]reco.Candidate, error) {
			return c.SampleMovieCandidates(ctx, 200)
		},
		"popular series": func(ctx context.Context, c reco.CatalogRepository) ([]reco.Candidate, error) {
			return c.SampleSeriesCandidates(ctx, 200)
		},
	}
	for name, d := range draws {
		t.Run(name, func(t *testing.T) {
			want, err := d(ctx, sqlCat)
			if err != nil {
				t.Fatalf("database: %v", err)
			}
			got, err := d(ctx, mem)
			if err != nil {
				t.Fatalf("memory: %v", err)
			}
			if len(want) == 0 {
				t.Skip("the database draw is empty")
			}
			if len(got) != len(want) {
				t.Fatalf("memory drew %d, database %d", len(got), len(want))
			}
			for i := range want {
				if got[i].MediaID != want[i].MediaID {
					t.Fatalf("at %d memory drew %d, database %d", i, got[i].MediaID, want[i].MediaID)
				}
				if !reflect.DeepEqual(got[i].Features, want[i].Features) {
					t.Fatalf("features of %d differ:\nmemory   %+v\ndatabase %+v", got[i].MediaID, got[i].Features, want[i].Features)
				}
			}
		})
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
	orch := newRecoOrchestrator(dbConn, openMemCatalog(t, dbConn), reco.DefaultConfig(), nil, recoSides{}).
		WithFeedCache(feedCache)
	t.Cleanup(orch.Wait)
	interactionsService := interactions.NewService(
		interactions.NewRepository(dbConn), tracking.NewRepository(dbConn)).
		WithFeedInvalidator(feedCache)

	first, err := orch.Feed(context.Background(), userID, reco.FeedAll, 30, 0)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("page 1 is empty; the synthetic user should get a Tier 2 feed")
	}

	ctx, rec := timing.NewContext(context.Background())
	if _, err := orch.Feed(ctx, userID, reco.FeedAll, 30, 30); err != nil {
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
	after, err := orch.Feed(ctx, userID, reco.FeedAll, 30, 0)
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

// newSeriesFixture writes media_index rows for a synthetic series and its
// first season, plus a user with nothing else, and returns all three ids. The
// cleanup drops the user's library and interactions before the index rows
// they point at.
func newSeriesFixture(t *testing.T, dbConn *sqlx.DB, tvID int) (userID uuid.UUID, series, season int) {
	t.Helper()
	ctx := context.Background()
	userID = uuid.New()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = dbConn.ExecContext(ctx, `delete from media_list where user_id = $1`, userID)
		_, _ = dbConn.ExecContext(ctx, `delete from users where id = $1`, userID)
		_, _ = dbConn.ExecContext(ctx, `delete from media_index where id = $1 and media_type in ('tv', 'season')`, tvID)
	})
	if err := dbConn.GetContext(ctx, &series, `
		insert into media_index (id, media_type) values ($1, 'tv') returning media_id
	`, tvID); err != nil {
		t.Fatalf("index series: %v", err)
	}
	if err := dbConn.GetContext(ctx, &season, `
		insert into media_index (id, season_number, media_type) values ($1, 1, 'season') returning media_id
	`, tvID); err != nil {
		t.Fatalf("index season: %v", err)
	}
	if err := users.NewRepository(dbConn).EnsureUser(ctx, userID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	return userID, series, season
}

// Tracking any season of a series, or any series-level interaction, makes
// the series seen, so the feed never offers a show the user is watching.
func TestSeenAdapter_ASeasonMarksItsSeriesSeen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	tests := []struct {
		name     string
		tvID     int
		onSeries bool   // the touch is on the series, not its season
		touch    string // $1 user, $2 the season or series media_id
	}{
		{"tracking a season", 2_100_000_060, false,
			`insert into media_list (user_id, media_id, media_type, status) values ($1, $2, 'season', 'watching')`},
		{"rating a season", 2_100_000_061, false,
			`insert into interactions (user_id, media_id, media_type, kind, rating) values ($1, $2, 'season', 'rated', 90)`},
		{"logging the series", 2_100_000_062, true,
			`insert into interactions (user_id, media_id, media_type, kind) values ($1, $2, 'tv', 'logged')`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID, series, season := newSeriesFixture(t, dbConn, tt.tvID)
			touched := season
			if tt.onSeries {
				touched = series
			}
			if _, err := dbConn.ExecContext(context.Background(), tt.touch, userID, touched); err != nil {
				t.Fatalf("touch: %v", err)
			}

			seen, err := newSeenAdapter(dbConn).SeenMediaIDs(context.Background(), userID)
			if err != nil {
				t.Fatalf("SeenMediaIDs: %v", err)
			}
			if !seen[series] {
				t.Errorf("seen = %v, want series %d in it", seen, series)
			}
		})
	}
}

// A season interaction carries its series' media_id, which the rollup keys
// the season's signal by.
func TestSignalAdapter_PointsASeasonAtItsSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	userID, series, season := newSeriesFixture(t, dbConn, 2_100_000_063)
	if _, err := dbConn.ExecContext(context.Background(), `
		insert into interactions (user_id, media_id, media_type, kind, rating)
		values ($1, $2, 'season', 'rated', 90)
	`, userID, season); err != nil {
		t.Fatalf("insert interaction: %v", err)
	}

	rows, err := newSignalAdapter(dbConn).ListByUser(context.Background(), userID, 0)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(rows) != 1 || rows[0].MediaID != season || rows[0].SeriesMediaID != series {
		t.Fatalf("rows = %+v, want season %d pointing at series %d", rows, season, series)
	}
}

// A history mixes films and series. Each comes back with its own type, and a
// series' combined genres arrive split into the film genres they merge.
func TestCatalogAdapter_LooksUpFilmsAndSeriesTogether(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	ctx := context.Background()

	var film, series int
	if err := dbConn.GetContext(ctx, &film, `
		select mi.media_id from media_index mi join movies m on m.movie_id = mi.id
		where mi.media_type = 'movie' limit 1
	`); err != nil {
		t.Skipf("no cached film: %v", err)
	}
	if err := dbConn.GetContext(ctx, &series, `
		select mi.media_id from media_index mi join tv_series s on s.tv_id = mi.id
		where mi.media_type = 'tv' and s.genre_ids && '{10759,10765,10768}'::int[] limit 1
	`); err != nil {
		t.Skipf("no cached series with a combined genre: %v", err)
	}

	cands, err := newCatalogAdapter(movies.NewRepository(dbConn), tv.NewRepository(dbConn)).
		CandidatesByMediaIDs(ctx, []int{film, series})
	if err != nil {
		t.Fatalf("CandidatesByMediaIDs: %v", err)
	}
	types := map[int]string{}
	for _, c := range cands {
		types[c.MediaID] = c.Features.MediaType
		if c.MediaType == "tv" {
			for _, g := range c.Features.GenreIDs {
				if g == 10759 || g == 10765 || g == 10768 {
					t.Errorf("series %d keeps combined genre %d; want it split into film genres", c.MediaID, g)
				}
			}
		}
	}
	if types[film] != "movie" || types[series] != "tv" {
		t.Errorf("types = %v, want film %d as movie and series %d as tv", types, film, series)
	}
}

// The pool draws series apart from films. Both series draws return shows,
// typed tv and keyed by the series' TMDB id, with features to score.
func TestCatalogAdapter_DrawsSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	ctx := context.Background()
	adapter := newCatalogAdapter(movies.NewRepository(dbConn), tv.NewRepository(dbConn))

	popular, err := adapter.SampleSeriesCandidates(ctx, 5)
	if err != nil {
		t.Fatalf("SampleSeriesCandidates: %v", err)
	}
	byTaste, err := adapter.SeriesCandidatesByTaste(ctx, []int{18}, nil, 25, 5)
	if err != nil {
		t.Fatalf("SeriesCandidatesByTaste: %v", err)
	}
	if len(popular) == 0 || len(byTaste) == 0 {
		t.Skipf("catalog drew %d popular and %d drama series; need some of each", len(popular), len(byTaste))
	}
	for _, c := range append(popular, byTaste...) {
		if c.MediaType != "tv" || c.TMDBID == 0 || c.Features == nil || c.Features.MediaType != "tv" {
			t.Errorf("drew %+v, want a series with its TMDB id and TV features", c)
		}
	}
}

// A ranked page mixes films and series, and each card reads its own cache: a
// runtime for the film, a season count for the series.
func TestCatalogAdapter_CardDetailsForFilmsAndSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	dbConn := openTestDB(t)
	ctx := context.Background()

	var film, series int
	if err := dbConn.GetContext(ctx, &film, `
		select mi.media_id from media_index mi join movies m on m.movie_id = mi.id
		where mi.media_type = 'movie' and m.runtime > 0 limit 1
	`); err != nil {
		t.Skipf("no cached film with a runtime: %v", err)
	}
	if err := dbConn.GetContext(ctx, &series, `
		select mi.media_id from media_index mi join tv_series s on s.tv_id = mi.id
		where mi.media_type = 'tv' and s.number_of_seasons > 0 and s.overview <> '' limit 1
	`); err != nil {
		t.Skipf("no cached series with seasons and a synopsis: %v", err)
	}

	details, err := newCatalogAdapter(movies.NewRepository(dbConn), tv.NewRepository(dbConn)).
		CardDetails(ctx, []int{film}, []int{series})
	if err != nil {
		t.Fatalf("CardDetails: %v", err)
	}
	if f := details[film]; f.Runtime == 0 || f.SeasonCount != 0 {
		t.Errorf("film card = %+v, want a runtime and no seasons", f)
	}
	if s := details[series]; s.SeasonCount == 0 || s.Runtime != 0 || s.Overview == "" {
		t.Errorf("series card = %+v, want a synopsis, seasons and no runtime", s)
	}
}
