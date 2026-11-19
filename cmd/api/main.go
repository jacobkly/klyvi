package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/config"
	"klyvi-api/internal/coliked"
	"klyvi-api/internal/family"
	"klyvi-api/internal/imports"
	"klyvi-api/internal/interactions"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/notifications"
	"klyvi-api/internal/onboarding"
	"klyvi-api/internal/opendata"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/router"
	"klyvi-api/internal/platform/supabase"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/plots"
	"klyvi-api/internal/providers"
	"klyvi-api/internal/rankings"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/search"
	"klyvi-api/internal/similar"
	"klyvi-api/internal/tracking"
	"klyvi-api/internal/tv"
	"klyvi-api/internal/users"
)

//  @title          Klyvi API
//  @version        1.0
//  @description    A RESTful API for managing movies, shows, and user media lists.
//                  Inspired by AniList, but focused on film and TV.

//  @contact.name   Jacob Klymenko
//  @contact.url    https://github.com/jacobkly

//  @license.name   MIT License
//  @license.url    https://opensource.org/licenses/MIT

// @host           localhost:8080
// @BasePath       /v1
func main() {
	cfg := config.New()

	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatal(err)
	}

	tmdbClient := tmdb.NewClient(cfg.TMDB.APIKey)
	// Cache catalog reads in-process; a cold key under load collapses to one
	// upstream call. Everything that reads TMDB goes through this wrapper.
	cachedTMDB := tmdb.NewCachingTMDBClient(tmdbClient)

	movieRepo := movies.NewRepository(dbConn)
	tvRepo := tv.NewRepository(dbConn)
	userRepo := users.NewRepository(dbConn)
	trackingRepo := tracking.NewRepository(dbConn)
	interactionsRepo := interactions.NewRepository(dbConn)
	onboardingRepo := onboarding.NewRepository(dbConn)

	movieService := movies.NewService(cachedTMDB, movieRepo)
	tvService := tv.NewService(cachedTMDB, tvRepo)
	searchService := search.NewService(cachedTMDB, movieRepo)
	// Nil when SUPABASE_URL / SUPABASE_SERVICE_ROLE_KEY are unset — the
	// avatar endpoints then answer with an explicit not-configured error
	// while everything else runs normally.
	supabaseClient := supabase.New(cfg.Supabase.URL, cfg.Supabase.ServiceRoleKey)
	if supabaseClient == nil {
		log.Println("supabase service-role client not configured; avatar endpoints disabled")
	}

	// Each user's ranked feed is kept for 10 minutes, so later pages and the
	// other screens that open on it reuse one computation. Writes that change
	// the user's signal drop it. A cached feed is about 130 KB, so 256 feeds
	// bound the cache near 33 MB. Each media filter of a user's feed is one.
	feedCache := reco.NewFeedCache(10*time.Minute, 256)

	trackingService := tracking.NewService(trackingRepo).
		WithFeedInvalidator(feedCache).
		WithSeriesEnsurer(tvService)
	interactionsService := interactions.NewService(interactionsRepo, trackingRepo).
		WithFeedInvalidator(feedCache).
		WithSeriesEnsurer(tvService).
		WithSeenMarker(trackingRepo)

	notificationsService := notifications.NewService(notifications.NewRepository(dbConn))

	userService := users.NewService(userRepo).
		WithExportSources(trackingService, interactionsService, notificationsService).
		WithFeedInvalidator(feedCache).
		WithAccountRecords(userRepo)
	if supabaseClient != nil {
		userService = userService.
			WithAvatarStorage(supabaseClient, cfg.Supabase.StorageBucket).
			WithAccountDeletion(supabaseClient, cfg.Supabase.StorageBucket)
	}
	onboardingService := onboarding.NewService(onboardingRepo, movieService)
	rankingsService := rankings.NewService(cachedTMDB, rankings.NewRepository(dbConn))
	// Imports are background work: their TMDB calls, including the detail
	// fetches behind EnsureCached, take a capped share so page requests
	// come first. Jobs a previous process left pending or running are dead,
	// so they are failed at startup and their owners can import again.
	importsTMDB := tmdb.NewBackgroundClient(cachedTMDB)
	importsRepo := imports.NewRepository(dbConn)
	if err := importsRepo.FailUnfinished(context.Background()); err != nil {
		log.Printf("imports: could not fail unfinished jobs: %v", err)
	}
	importsService := imports.NewService(importsRepo, importsTMDB,
		movies.NewService(importsTMDB, movieRepo), trackingService, interactionsService).
		WithSeries(tv.NewService(importsTMDB, tvRepo))

	// Recommender orchestrator. Adapters wrap movies.Repository + raw SQL
	// against interactions/media_list to feed the orchestrator. Scoring
	// weights come from the harness-tuned defaults, overridable via RECO_* env.
	recoCfg := reco.DefaultConfig()
	recoCfg.KeywordWeight = cfg.Reco.KeywordWeight
	recoCfg.GenreWeight = cfg.Reco.GenreWeight
	recoCfg.EraWeight = cfg.Reco.EraWeight
	recoCfg.NegativeSimWeight = cfg.Reco.NegativeSimWeight
	recoCfg.QualityFloorVotes = cfg.Reco.QualityFloorVotes
	recoCfg.TVShare = cfg.Reco.TVShare
	recoCfg.CalibrationWeight = cfg.Reco.CalibrationWeight
	recoCfg.FactWeight = cfg.Reco.FactWeight
	recoCfg.CrewWeight = cfg.Reco.CrewWeight
	recoCfg.RelatedKeywordWeight = cfg.Reco.RelatedKeywordWeight
	recoCfg.WalkWeight = cfg.Reco.WalkWeight
	recoCfg.WalkBeta = cfg.Reco.WalkBeta
	recoCfg.CoLikedWeight = cfg.Reco.CoLikedWeight
	recoCfg.CoLikedShrink = cfg.Reco.CoLikedShrink
	recoCfg.PlotWeight = cfg.Reco.PlotWeight

	// Global IDF tables, gated by RECO_USE_IDF and loaded once at startup: they
	// damp catalog-ubiquitous features so a diverse taste leans on its
	// discriminative signal. Off by default (the offline sweep found it a wash);
	// when on it needs feature_stats populated, else a type's table is nil
	// (identity) and its scoring is unchanged. Films and series each get their
	// own table. A catalog reseed needs a restart to pick up refreshed
	// weights — the lifecycle the rest of the catalog follows.
	//
	// The "because you liked X" reasons always weigh shared keywords by IDF,
	// so the tables load either way, and scoring reads them only when on.
	featureTables := &reco.MediaIDF{
		Movie: recoFeatureIDF(movieRepo, "movie", reco.NewFeatureIDF),
		TV:    recoFeatureIDF(movieRepo, "tv", reco.NewTVFeatureIDF),
	}
	var featureIDF *reco.MediaIDF
	if cfg.Reco.UseIDF {
		featureIDF = featureTables
		log.Println("reco: IDF weighting on in scoring")
	} else {
		log.Println("reco: IDF weighting off in scoring (set RECO_USE_IDF=true to enable), on for reasons")
	}

	// The feed draws its candidate pool from an in-memory copy of the catalog,
	// loaded here and refreshed in the background, instead of shipping about
	// 1,200 rows from the database per request. A failed first load leaves
	// the feed on the database until a refresh succeeds.
	var recoCatalog reco.CatalogRepository = newCatalogAdapter(movieRepo, tvRepo)
	var memCatalog *cachedCatalog
	if cfg.Reco.CatalogRefresh > 0 {
		memCatalog = newCachedCatalog(recoCatalog, repoCatalogLoader(movieRepo, tvRepo))
		loadCtx, cancelLoad := context.WithTimeout(context.Background(), time.Minute)
		started := time.Now()
		if err := memCatalog.Refresh(loadCtx); err != nil {
			log.Printf("reco catalog: first load failed, the feed reads the database until a refresh works: %v", err)
		} else {
			films, series := memCatalog.Size()
			log.Printf("reco catalog: %d films and %d series in memory, loaded in %v", films, series, time.Since(started).Round(time.Millisecond))
		}
		cancelLoad()
		recoCatalog = memCatalog
	} else {
		log.Println("reco catalog: in-memory catalog off (RECO_CATALOG_REFRESH=0), the feed reads the database")
	}

	// The Wikidata facts, the crew, the related keywords and the walk load
	// only when their term is on. Like the IDF tables, they are read once, so
	// a title cached later counts toward none of them until the next restart.
	var sides recoSides
	var cast *reco.FactIndex
	if recoCfg.FactWeight > 0 {
		sides.facts = loadRecoFacts(dbConn)
	}
	if recoCfg.CrewWeight > 0 {
		sides.crew = loadRecoCrew(movieRepo, tvRepo)
	}
	if recoCfg.WalkWeight > 0 {
		cast, sides.walk = loadRecoWalk(movieRepo, tvRepo)
	}
	var plotVecs *reco.PlotIndex
	if recoCfg.PlotWeight > 0 || cfg.Reco.SimilarPlotWeight > 0 {
		plotVecs = loadRecoPlots(dbConn)
	}
	feedPlots := plotVecs
	if recoCfg.PlotWeight <= 0 {
		feedPlots = nil // loaded for the shelves alone
	}
	if sides.facts != nil || sides.crew != nil || cast != nil || feedPlots != nil {
		recoCatalog = reco.NewFactCatalog(recoCatalog, reco.CatalogSides{Facts: sides.facts, Crew: sides.crew, Cast: cast, Plots: feedPlots})
	}
	if recoCfg.RelatedKeywordWeight > 0 {
		sides.related = loadRelatedKeywords(movieRepo, tvRepo)
	}
	// The co-liked counts are read per request, so the nightly rebuild
	// reaches the feed without a restart.
	if recoCfg.CoLikedWeight > 0 {
		sides.coLiked = coliked.NewRepository(dbConn)
	}

	recoOrchestrator := newRecoOrchestrator(dbConn, recoCatalog, recoCfg, featureIDF, sides).
		WithReasonIDF(featureTables).
		WithFeedCache(feedCache)

	// The similar shelves always weigh a feature by how rare it is: a shared
	// "based on novel" says little about two titles, a shared "class
	// differences" says a lot. Without feature stats they weigh every
	// feature the same.
	similarCfg := similar.Config{
		KeywordWeight: recoCfg.KeywordWeight,
		GenreWeight:   recoCfg.GenreWeight,
		MinVotes:      recoCfg.QualityFloorVotes,
	}
	similarMovies := similar.NewMovieService(similar.NewMovieCatalog(dbConn), movieService.EnsureCached,
		cachedTMDB, loadFeatureIDF(movieRepo, "movie"), similarCfg).
		WithPlots(plotVecs, cfg.Reco.SimilarPlotWeight)
	similarTV := similar.NewTVService(similar.NewTVCatalog(dbConn), tvService.EnsureSeries,
		cachedTMDB, loadFeatureIDF(movieRepo, "tv"), similarCfg).
		WithPlots(plotVecs, cfg.Reco.SimilarPlotWeight)

	authMW, err := middleware.NewAuthMiddleware(middleware.AuthConfig{
		JWKSURL:  cfg.Supabase.JWKSURL,
		Issuer:   cfg.Supabase.Issuer,
		Audience: cfg.Supabase.Audience,
	})
	if err != nil {
		log.Fatalf("auth middleware: %v", err)
	}

	r := router.New(router.Services{
		Movies:         movieService,
		TV:             tvService,
		Search:         searchService,
		Users:          userService,
		Tracking:       trackingService,
		Interactions:   interactionsService,
		Reco:           recoOrchestrator,
		Onboarding:     onboardingService,
		Rankings:       rankingsService,
		Imports:        importsService,
		Notifications:  notificationsService,
		SimilarMovies:  similarMovies,
		SimilarTV:      similarTV,
		Family:         family.NewRepository(dbConn),
		Providers:      providers.NewService(cachedTMDB, providers.NewRepository(dbConn)),
		AuthMW:         authMW,
		DB:             dbConn,
		AllowedOrigins: cfg.Server.AllowedOrigins,
		RateLimitRPM:   cfg.Server.RateLimitRPM,
		RateLimitBurst: cfg.Server.RateLimitBurst,
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: r,
		// Headers get their own short limit, so a client that trickles them
		// in cannot hold a connection open. Uploads extend the body read per
		// route (middleware.LongRequest).
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.Server.TimeoutRead,
		WriteTimeout:      cfg.Server.TimeoutWrite,
		IdleTimeout:       cfg.Server.TimeoutIdle,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if memCatalog != nil {
		go memCatalog.Run(ctx, cfg.Reco.CatalogRefresh)
	}

	go func() {
		log.Println("Starting server on", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received, draining...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}
	// Let in-flight search cache writes and taste profile saves land before
	// the pool closes.
	searchService.Wait()
	recoOrchestrator.Wait()
	log.Println("server stopped")
}

// recoFeatureIDF reads one media type's feature stats into the feed's IDF
// table, built by build. A failed load stops the server, since RECO_USE_IDF
// asked for the weighting. An empty one returns nil, which weighs every
// feature of that type the same.
func recoFeatureIDF(repo *movies.Repository, mediaType string,
	build func(genreDF, keywordDF map[int]int, corpus int) *reco.FeatureIDF) *reco.FeatureIDF {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	genreDF, keywordDF, corpus, err := repo.LoadFeatureStats(ctx, mediaType)
	if err != nil {
		// Unweighted scoring and reasons still work, so this is not fatal.
		log.Printf("reco: load %s feature stats, weighing every feature the same: %v", mediaType, err)
		return nil
	}
	idf := build(genreDF, keywordDF, corpus)
	if idf == nil {
		log.Printf("reco: %s feature_stats empty, run seed-catalog -refresh-feature-stats", mediaType)
	} else {
		log.Printf("reco: %s IDF tables loaded (corpus=%d genres=%d keywords=%d)", mediaType, corpus, len(genreDF), len(keywordDF))
	}
	return idf
}

// loadRecoFacts reads the Wikidata facts the feed matches titles on. A failed
// or empty load returns an empty index, so every title scores as it would
// with the term off.
func loadRecoFacts(dbConn *sqlx.DB) *reco.FactIndex {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	titles, err := opendata.NewRepository(dbConn).RecoFacts(ctx)
	if err != nil {
		log.Printf("reco: load wikidata facts: %v", err)
	}
	facts := reco.NewFactIndex(titles)
	if facts.Len() == 0 {
		log.Println("reco: no wikidata facts, run /opendata (the fact term scores nothing until then)")
	} else {
		log.Printf("reco: wikidata facts loaded for %d titles", facts.Len())
	}
	return facts
}

// loadRecoPlots reads the Wikipedia plot vectors the plot term compares
// stories by. A failed or empty load returns an empty index, so every title
// scores as it would with the term off.
func loadRecoPlots(dbConn *sqlx.DB) *reco.PlotIndex {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	vecs, err := plots.NewRepository(dbConn).Vectors(ctx)
	if err != nil {
		log.Printf("reco: load plot vectors: %v", err)
	}
	titles := make([]reco.TitlePlot, len(vecs))
	for i, v := range vecs {
		titles[i] = reco.TitlePlot{MediaType: v.MediaType, TMDBID: v.TMDBID, Vector: v.Embedding}
	}
	idx := reco.NewPlotIndex(titles)
	if idx.Len() == 0 {
		log.Println("reco: no plot vectors, run /plots (the plot term scores nothing until then)")
	} else {
		log.Printf("reco: plot vectors loaded for %d titles", idx.Len())
	}
	return idx
}

// loadRecoCrew reads the crew the feed matches titles on. A failed load
// returns an empty index, so every title scores as it would with the term off.
func loadRecoCrew(films, series reco.CrewSource) *reco.FactIndex {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	crew, err := reco.LoadCrewIndex(ctx, films, series)
	if err != nil {
		log.Printf("reco: %v", err)
		return reco.NewFactIndex(nil)
	}
	log.Printf("reco: crew loaded for %d titles", crew.Len())
	return crew
}

// walkSource is what the walk reads from each media type's repository.
type walkSource interface {
	reco.CastSource
	reco.KeywordSource
}

// loadRecoWalk reads the top-billed cast and counts the walk's node degrees
// over it and the keywords. A failed load returns an empty cast and a nil
// graph, so the walk steps through keywords alone, each counted once.
func loadRecoWalk(films, series walkSource) (*reco.FactIndex, *reco.WalkGraph) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cast, err := reco.LoadCastIndex(ctx, films, series)
	if err != nil {
		log.Printf("reco: %v", err)
		return reco.NewFactIndex(nil), nil
	}
	var keywords [][]int
	for _, src := range []reco.KeywordSource{films, series} {
		sets, err := src.RecoKeywordSets(ctx)
		if err != nil {
			log.Printf("reco: walk keywords: %v", err)
			return cast, nil
		}
		keywords = append(keywords, sets...)
	}
	g := reco.NewWalkGraph(keywords, cast.Sets())
	log.Printf("reco: walk graph holds %d keywords and cast members, cast for %d titles", g.Len(), cast.Len())
	return cast, g
}

// loadRelatedKeywords counts which keywords tag the same titles. A failed load
// returns an empty index, so every title scores as it would with the term off.
func loadRelatedKeywords(films, series reco.KeywordSource) *reco.RelatedKeywords {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	related, err := reco.LoadRelatedKeywords(ctx, films, series)
	if err != nil {
		log.Printf("reco: %v", err)
		return reco.NewRelatedKeywords(nil)
	}
	log.Printf("reco: related keywords counted for %d keywords", related.Len())
	return related
}

// loadFeatureIDF reads one media type's feature stats into an IDF table. A
// failed or empty load returns nil, which weighs every feature the same.
func loadFeatureIDF(repo *movies.Repository, mediaType string) *reco.FeatureIDF {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	genreDF, keywordDF, corpus, err := repo.LoadFeatureStats(ctx, mediaType)
	if err != nil {
		log.Printf("similar: load %s feature stats: %v", mediaType, err)
		return nil
	}
	idf := reco.NewFeatureIDF(genreDF, keywordDF, corpus)
	if idf == nil {
		log.Printf("similar: no %s feature stats, run seed-catalog -refresh-feature-stats", mediaType)
	}
	return idf
}
