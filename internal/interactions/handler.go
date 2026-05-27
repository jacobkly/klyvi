package interactions

import (
	"encoding/json"
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

// Record — POST /v1/interactions.
func (a *API) Record(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var req RecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	rec, err := a.service.Record(r.Context(), userID, req)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.WriteSuccess(w, http.StatusCreated, "v1",
		middleware.StatsFromContext(r.Context()), rec)
}

// List — GET /v1/interactions?since_days=30
//
// Returns the authenticated user's interaction history, most recent first.
// An optional `since_days` query param bounds the window (omit for all-time).
func (a *API) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var sinceDays int
	if v := r.URL.Query().Get("since_days"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			response.WriteError(w, http.StatusBadRequest, "since_days must be a non-negative integer")
			return
		}
		sinceDays = parsed
	}

	rows, err := a.service.ListByUser(r.Context(), userID, sinceDays)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), rows)
}
