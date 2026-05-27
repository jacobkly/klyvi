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
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/router"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/interactions"
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

	movieRepo := movies.NewRepository(dbConn)
	tvRepo := tv.NewRepository(dbConn)
	userRepo := users.NewRepository(dbConn)
	trackingRepo := tracking.NewRepository(dbConn)
	interactionsRepo := interactions.NewRepository(dbConn)

	movieService := movies.NewService(tmdbClient, movieRepo)
	tvService := tv.NewService(tmdbClient, tvRepo)
	searchService := search.NewService(tmdbClient, movieRepo)
	userService := users.NewService(userRepo)
	trackingService := tracking.NewService(trackingRepo)
	interactionsService := interactions.NewService(interactionsRepo, trackingRepo)

	// Recommender orchestrator. Adapters wrap movies.Repository + raw SQL
	// against interactions/media_list to feed the orchestrator. Tier 0 is
	// always registered; Tier 1/2 land in subsequent slices.
	recoOrchestrator := reco.NewOrchestrator(
		newCatalogAdapter(movieRepo),
		newSignalAdapter(dbConn),
		newSeenAdapter(dbConn),
		reco.DefaultConfig(),
	).WithTier0(reco.NewTier0()).
		WithTier1(reco.NewTier1(reco.DefaultConfig())).
		WithTier2(reco.NewTier2(reco.DefaultConfig(), newProfileAdapter(dbConn)))

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
		AuthMW:         authMW,
		AllowedOrigins: cfg.Server.AllowedOrigins,
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
