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

	rec, err := a.service.RecordFromClient(r.Context(), userID, req)
	if err != nil {
		respondError(w, err)
		return
	}

	response.WriteSuccess(w, http.StatusCreated, "v1",
		middleware.StatsFromContext(r.Context()), rec)
}

// RecordBatch — POST /v1/interactions/batch. Body `{ "interactions": [...] }`,
// capped at MaxBatchSize. Per-row accept/reject; the response reports counts.
func (a *API) RecordBatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var req struct {
		Interactions []RecordRequest `json:"interactions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	res, err := a.service.RecordBatch(r.Context(), userID, req.Interactions)
	if err != nil {
		respondError(w, err)
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), res)
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
		respondError(w, err)
		return
	}
	// Guarantee a JSON array (not null) for callers; OpenAPI declares the
	// field as an array.
	if rows == nil {
		rows = []Interaction{}
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), rows)
}
