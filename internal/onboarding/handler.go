package onboarding

import (
	"net/http"
	"strconv"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

type API struct {
	service *Service
}

func NewAPI(service *Service) *API {
	return &API{service: service}
}

// Pool — GET /v1/onboarding/pool?limit=N
//
// Public endpoint (no auth). Returns the active onboarding pool enriched
// with cached movie data so the frontend can render the curated swipe
// deck without per-film follow-up lookups.
func (a *API) Pool(w http.ResponseWriter, r *http.Request) {
	limit := 20 // default per the task spec
	if v := r.URL.Query().Get("limit"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			response.WriteError(w, http.StatusBadRequest, "limit must be a non-negative integer")
			return
		}
		limit = parsed
	}

	entries, err := a.service.ListEnriched(r.Context(), limit)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), entries)
}
