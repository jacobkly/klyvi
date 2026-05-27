package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"klyvi-api/internal/health"
	"klyvi-api/internal/interactions"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/http/middleware"
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

	// AuthMW verifies the Supabase JWT and puts the user UUID into context.
	// Mounted on all protected route groups.
	AuthMW func(http.Handler) http.Handler
}

func New(services Services) *chi.Mux {
	r := chi.NewRouter()

	r.Use(chimiddleware.RequestID)
	r.Use(middleware.LoggerMiddleware)
	r.Use(middleware.StatsMiddleware)

	r.Get("/health", health.Get)

	r.Route("/v1", func(r chi.Router) {
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

		searchAPI := search.NewAPI(services.Search)
		r.Get("/search", searchAPI.GetSearchResult)

		// --- Protected routes: auth required, user row auto-upserted on
		// first successful authentication.
		r.Group(func(r chi.Router) {
			r.Use(services.AuthMW)
			r.Use(services.Users.EnsureUserMiddleware())

			usersAPI := users.NewAPI(services.Users)
			r.Get("/users/me", usersAPI.GetMe)

			trackingAPI := tracking.NewAPI(services.Tracking)
			r.Route("/tracking", func(r chi.Router) {
				r.Get("/", trackingAPI.List)
				r.Post("/", trackingAPI.Add)
				r.Patch("/{media_id}", trackingAPI.Update)
				r.Delete("/{media_id}", trackingAPI.Delete)
			})

			interactionsAPI := interactions.NewAPI(services.Interactions)
			r.Post("/interactions", interactionsAPI.Record)

			recoAPI := reco.NewAPI(services.Reco)
			r.Get("/reco/feed", recoAPI.Feed)
		})
	})

	return r
}
