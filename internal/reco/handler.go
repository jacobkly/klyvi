package reco

import (
	"net/http"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

type API struct {
	orch *Orchestrator
}

func NewAPI(orch *Orchestrator) *API {
	return &API{orch: orch}
}

// Feed — GET /v1/reco/feed. Returns the top-K recommendations for the
// authenticated user. UUID is read from request context (placed there by
// the JWT middleware); no user id is accepted from the request.
func (a *API) Feed(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	scored, err := a.orch.Feed(r.Context(), userID)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), scored)
}
