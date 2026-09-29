package similar

import (
	"errors"
	"log"
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

// Get — GET /v1/{movies,tv}/{id}/similar?limit=N (public). Up to limit
// titles like the one with TMDB id {id}, best first. limit defaults to
// DefaultLimit and is capped at MaxLimit.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id <= 0 {
		response.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	limit := DefaultLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		limit, err = strconv.Atoi(s)
		if err != nil || limit <= 0 {
			response.WriteError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
	}

	items, err := a.service.Similar(r.Context(), id, limit)
	if errors.Is(err, ErrNotFound) {
		response.WriteError(w, http.StatusNotFound, "title not found")
		return
	}
	if err != nil {
		log.Printf("similar: %v", err)
		response.WriteError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), items)
}
