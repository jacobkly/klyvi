package tracking

import (
	"encoding/json"
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

// Add — POST /v1/tracking.
func (a *API) Add(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var req AddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	entry, err := a.service.Add(r.Context(), userID, req)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.WriteSuccess(w, http.StatusCreated, "v1",
		middleware.StatsFromContext(r.Context()), entry)
}

// Update — PATCH /v1/tracking/{media_id}.
func (a *API) Update(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	mediaID, err := strconv.Atoi(chi.URLParam(r, "media_id"))
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid media_id")
		return
	}

	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	entry, err := a.service.Update(r.Context(), userID, mediaID, req)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if entry == nil {
		response.WriteError(w, http.StatusNotFound, "tracking entry not found")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), entry)
}

// Delete — DELETE /v1/tracking/{media_id}.
func (a *API) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	mediaID, err := strconv.Atoi(chi.URLParam(r, "media_id"))
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid media_id")
		return
	}

	deleted, err := a.service.Delete(r.Context(), userID, mediaID)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		response.WriteError(w, http.StatusNotFound, "tracking entry not found")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()),
		map[string]bool{"deleted": true})
}

// List — GET /v1/tracking?media_type=movie&status=completed
func (a *API) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	q := r.URL.Query()
	filters := ListFilters{}
	if v := q.Get("media_type"); v != "" {
		filters.MediaType = &v
	}
	if v := q.Get("status"); v != "" {
		filters.Status = &v
	}

	entries, err := a.service.List(r.Context(), userID, filters)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), entries)
}
