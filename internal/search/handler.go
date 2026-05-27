package search

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

func (a *API) GetSearchResult(w http.ResponseWriter, r *http.Request) {
	queryParams := r.URL.Query()
	searchType := queryParams.Get("type")
	query := queryParams.Get("query")
	if query == "" {
		response.WriteError(w, http.StatusBadRequest, "query is required")
		return
	}

	searchResult, err := a.service.GetSearchResult(r.Context(), searchType, query)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		searchResult,
	)
}
