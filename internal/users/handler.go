package users

import (
	"encoding/json"
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

// GetMe returns the authenticated user's row. The user UUID comes from
// request context — never from the URL or body — and is put there by the
// JWT auth middleware. By the time this handler runs, EnsureUserMiddleware
// has already upserted the row, so a missing-row response indicates a real
// data inconsistency.
func (a *API) GetMe(w http.ResponseWriter, r *http.Request) {
	id, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	user, err := a.service.GetMe(r.Context(), id)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		response.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		user,
	)
}

// UpdateMe — PATCH /v1/users/me. Partial profile update.
func (a *API) UpdateMe(w http.ResponseWriter, r *http.Request) {
	id, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var req UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	user, err := a.service.UpdateMe(r.Context(), id, req)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if user == nil {
		response.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), user)
}
