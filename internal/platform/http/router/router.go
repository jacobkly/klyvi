package router

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"klyvi-api/internal/health"
	"klyvi-api/internal/imports"
	"klyvi-api/internal/interactions"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/onboarding"
	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/rankings"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/search"
	"klyvi-api/internal/tracking"
	"klyvi-api/internal/tv"
	"klyvi-api/internal/users"
)

type Services struct {
	Movies       *movies.Service
	TV           *tv.Service
	Search       *search.Service
	Users        *users.Service
	Tracking     *tracking.Service
	Interactions *interactions.Service
	Reco         *reco.Orchestrator
	Onboarding   *onboarding.Service
	Rankings     *rankings.Service
	Imports      *imports.Service

	// AuthMW verifies the Supabase JWT and puts the user UUID into context.
	// Mounted on all protected route groups.
	AuthMW func(http.Handler) http.Handler

	// AllowedOrigins is the comma-separated CORS origin list from config.
	// Empty string falls back to localhost dev defaults.
	AllowedOrigins string

	// RateLimitRPM / RateLimitBurst configure the global per-IP limiter mounted
	// on the /v1 group.
	RateLimitRPM   int
	RateLimitBurst int
}

func New(services Services) *chi.Mux {
	r := chi.NewRouter()

	r.Use(chimiddleware.RequestID)
	// Recover panics into a 500 before anything else runs, so a handler bug
	// never drops the connection. Sits after RequestID so the log carries the id.
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   parseOrigins(services.AllowedOrigins),
		AllowedMethods:   []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-Id"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(middleware.LoggerMiddleware)
	r.Use(middleware.StatsMiddleware)

	r.Get("/health", health.Get)

	r.Route("/v1", func(r chi.Router) {
		// Global per-IP rate limit for the whole API. /health sits on the root
		// router above and is deliberately left unthrottled for uptime checks.
		r.Use(middleware.RateLimit(middleware.RateLimitConfig{
			RequestsPerMinute: services.RateLimitRPM,
			Burst:             services.RateLimitBurst,
		}))

		// --- Public catalog routes: catalog/search work without auth.
		movieAPI := movies.NewAPI(services.Movies)
		r.Route("/movies", func(r chi.Router) {
			r.Get("/{id}", movieAPI.GetMovieById)
			r.Get("/{id}/recommendations", movieAPI.GetMovieRecommendations)
			r.Get("/{id}/collection", movieAPI.GetMovieCollection)
			r.Get("/", movieAPI.GetMovieList)
		})

		tvAPI := tv.NewAPI(services.TV)
		r.Route("/tv", func(r chi.Router) {
			r.Get("/{id}", tvAPI.GetTvById)
			r.Get("/{id}/recommendations", tvAPI.GetTvRecommendations)
			r.Get("/{id}/collection", tvAPI.GetTvCollection)
			r.Get("/", tvAPI.GetTvList)
		})

		// Search is capped tighter than the global limit: it is the least
		// cacheable public endpoint and the most expensive per call.
		searchAPI := search.NewAPI(services.Search)
		r.With(middleware.RateLimit(middleware.RateLimitConfig{
			RequestsPerMinute: 60,
			Burst:             20,
		})).Get("/search", searchAPI.GetSearchResult)

		// Onboarding pool is public — the frontend hits it before the user
		// has authenticated (the swipe deck on the welcome screen).
		onboardingAPI := onboarding.NewAPI(services.Onboarding)
		r.Get("/onboarding/pool", onboardingAPI.Pool)

		// Username availability is public for the same reason: sign-up has
		// no session yet. It reads the users table, so it carries its own
		// rate limit rather than relying on auth to keep the volume sane.
		usersPublicAPI := users.NewAPI(services.Users)
		r.With(middleware.RateLimit(middleware.RateLimitConfig{
			RequestsPerMinute: 20,
			Burst:             10,
		})).Get("/users/username-available", usersPublicAPI.CheckUsernameAvailable)

		// Top-100 rankings are public and cached server-side for 24h.
		rankingsAPI := rankings.NewAPI(services.Rankings)
		r.Route("/rankings", func(r chi.Router) {
			r.Get("/movies", rankingsAPI.Movies)
			r.Get("/tv", rankingsAPI.TV)
		})

		// --- Protected routes: auth required, user row auto-upserted on
		// first successful authentication.
		r.Group(func(r chi.Router) {
			r.Use(services.AuthMW)
			r.Use(services.Users.EnsureUserMiddleware())
			r.Use(middleware.UserLoggerMiddleware)

			usersAPI := users.NewAPI(services.Users)
			r.Get("/users/me", usersAPI.GetMe)
			r.Patch("/users/me", usersAPI.UpdateMe)
			r.Delete("/users/me", usersAPI.DeleteMe)
			r.Post("/users/me/avatar", usersAPI.UploadAvatar)
			r.Delete("/users/me/avatar", usersAPI.DeleteAvatar)
			r.Get("/users/me/stats", usersAPI.GetStats)
			r.Get("/users/me/export", usersAPI.ExportTracking)
			r.Get("/users/me/data", usersAPI.ExportAccountData)

			trackingAPI := tracking.NewAPI(services.Tracking)
			r.Route("/tracking", func(r chi.Router) {
				r.Get("/", trackingAPI.List)
				r.Post("/", trackingAPI.Add)
				// Scoped bulk delete of the whole list; the single-entry
				// delete lives on /{media_id} below and does not collide.
				r.Delete("/", trackingAPI.DeleteList)
				// Static segment — chi matches it ahead of /{media_id}.
				r.Post("/reset-scores", trackingAPI.ResetScores)
				r.Patch("/{media_id}", trackingAPI.Update)
				r.Delete("/{media_id}", trackingAPI.Delete)
			})

			interactionsAPI := interactions.NewAPI(services.Interactions)
			r.Route("/interactions", func(r chi.Router) {
				r.Post("/", interactionsAPI.Record)
				r.Post("/batch", interactionsAPI.RecordBatch)
				r.Get("/", interactionsAPI.List)
			})

			recoAPI := reco.NewAPI(services.Reco)
			r.Get("/reco/feed", recoAPI.Feed)

			importsAPI := imports.NewAPI(services.Imports)
			r.Route("/imports", func(r chi.Router) {
				r.Post("/", importsAPI.Create)
				r.Get("/{id}", importsAPI.Get)
			})
		})
	})

	return r
}

// parseOrigins turns the comma-separated env var into a slice. Empty input
// falls back to common localhost dev origins so a fresh setup works
// without configuration.
func parseOrigins(raw string) []string {
	if raw == "" {
		return []string{
			"http://localhost:3000",
			"http://localhost:5173",
			"http://127.0.0.1:3000",
			"http://127.0.0.1:5173",
		}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
