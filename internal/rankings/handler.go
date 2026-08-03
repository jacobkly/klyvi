package rankings

import (
	"net/http"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

type API struct {
	service *Service
}

func NewAPI(service *Service) *API {
	return &API{service: service}
}

// Movies — GET /v1/rankings/movies (public). Up to 100 ranked movies.
func (a *API) Movies(w http.ResponseWriter, r *http.Request) {
	rankings, err := a.service.Movies(r.Context())
	if err != nil {
		response.WriteError(w, http.StatusBadGateway, "could not load rankings")
		return
	}
	if rankings == nil {
		rankings = []Ranking{}
	}
	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), rankings)
}

// TV — GET /v1/rankings/tv (public). Up to 100 ranked TV series.
func (a *API) TV(w http.ResponseWriter, r *http.Request) {
	rankings, err := a.service.TV(r.Context())
	if err != nil {
		response.WriteError(w, http.StatusBadGateway, "could not load rankings")
		return
	}
	if rankings == nil {
		rankings = []Ranking{}
	}
	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), rankings)
}
