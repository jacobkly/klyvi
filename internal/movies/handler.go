package movies

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

type API struct {
	service *Service
}

func NewAPI(service *Service) *API {
	return &API{service: service}
}

func (a *API) GetMovieById(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}

	idType := r.URL.Query().Get("id_type")
	if idType == "" {
		response.WriteError(w, http.StatusBadRequest, "id_type is required (tmdb | media)")
		return
	}

	movie, err := a.service.GetMovieById(r.Context(), id, idType)
	if err != nil {
		respondError(w, err)
		return
	}

	stats := middleware.StatsFromContext(r.Context())

	response.WriteSuccess(w, http.StatusOK, "v1", stats, movie)
}

func (a *API) GetMovieCollection(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid movie id")
		return
	}

	collections, err := a.service.GetMovieCollection(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}

	if len(collections) == 0 {
		response.WriteSuccess(
			w,
			http.StatusOK,
			"v1",
			middleware.StatsFromContext(r.Context()),
			map[string]string{"details": "No collection found"},
		)
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		collections,
	)
}

func (a *API) GetTrendingMovies(w http.ResponseWriter, r *http.Request) {
	trending, err := a.service.GetTrendingMovies(r.Context())
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		trending,
	)
}

func (a *API) GetUpcomingMovies(w http.ResponseWriter, r *http.Request) {
	trending, err := a.service.GetUpcomingMovies(r.Context())
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		trending,
	)
}

func (a *API) GetPopularMovies(w http.ResponseWriter, r *http.Request) {
	trending, err := a.service.GetPopularMovies(r.Context())
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		trending,
	)
}

func (a *API) GetTopRatedMovies(w http.ResponseWriter, r *http.Request) {
	trending, err := a.service.GetTopRatedMovies(r.Context())
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		trending,
	)
}

func (a *API) GetMovieList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	listType := query.Get("type")

	// Absent or non-numeric page falls back to 1; the service clamps range.
	page, _ := strconv.Atoi(query.Get("page"))

	data, err := a.service.GetMovieList(r.Context(), listType, page)
	if err != nil {
		respondError(w, err)
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		data,
	)
}
