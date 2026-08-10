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

	"klyvi-api/config"
	"klyvi-api/internal/imports"
	"klyvi-api/internal/interactions"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/onboarding"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/router"
	"klyvi-api/internal/platform/supabase"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/rankings"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/search"
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

	trackingService := tracking.NewService(trackingRepo)
	interactionsService := interactions.NewService(interactionsRepo, trackingRepo)

	userService := users.NewService(userRepo).
		WithExportSources(trackingService, interactionsService)
	if supabaseClient != nil {
		userService = userService.
			WithAvatarStorage(supabaseClient, cfg.Supabase.StorageBucket).
			WithAccountDeletion(supabaseClient, cfg.Supabase.StorageBucket)
	}
	onboardingService := onboarding.NewService(onboardingRepo, movieService)
	rankingsService := rankings.NewService(cachedTMDB, rankings.NewRepository(dbConn))
	importsService := imports.NewService(
		imports.NewRepository(dbConn), cachedTMDB, trackingService, interactionsService)

	// Recommender orchestrator. Adapters wrap movies.Repository + raw SQL
	// against interactions/media_list to feed the orchestrator. Scoring
	// weights come from the harness-tuned defaults, overridable via RECO_* env.
	recoCfg := reco.DefaultConfig()
	recoCfg.KeywordWeight = cfg.Reco.KeywordWeight
	recoCfg.GenreWeight = cfg.Reco.GenreWeight
	recoCfg.EraWeight = cfg.Reco.EraWeight
	recoCfg.NegativeSimWeight = cfg.Reco.NegativeSimWeight
	recoCfg.QualityFloorVotes = cfg.Reco.QualityFloorVotes

	recoOrchestrator := reco.NewOrchestrator(
		newCatalogAdapter(movieRepo),
		newSignalAdapter(dbConn),
		newSeenAdapter(dbConn),
		recoCfg,
	).WithTier0(reco.NewTier0()).
		WithTier1(reco.NewTier1(recoCfg)).
		WithTier2(reco.NewTier2(recoCfg, newProfileAdapter(dbConn)))

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
		AuthMW:         authMW,
		AllowedOrigins: cfg.Server.AllowedOrigins,
		RateLimitRPM:   cfg.Server.RateLimitRPM,
		RateLimitBurst: cfg.Server.RateLimitBurst,
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      r,
		ReadTimeout:  cfg.Server.TimeoutRead,
		WriteTimeout: cfg.Server.TimeoutWrite,
		IdleTimeout:  cfg.Server.TimeoutIdle,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
	log.Println("server stopped")
}
