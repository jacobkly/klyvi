package users

import (
	"encoding/json"
	"errors"
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
		response.WriteError(w, statusForProfileError(err), err.Error())
		return
	}
	if user == nil {
		response.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), user)
}

// CheckUsernameAvailable — GET /v1/users/username-available?username=<name>.
//
// Public by necessity: sign-up has no session yet, so this cannot sit behind
// the JWT middleware. It is rate-limited at the route instead, because an
// uncapped unauthenticated read of the users table is a free enumerator.
//
// An unusable name is a 200 with available=false, not a 4xx — "is this name
// free" was answered successfully even when the answer is no. Only a
// malformed request (no username at all) is a 400.
func (a *API) CheckUsernameAvailable(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("username")
	if name == "" {
		response.WriteError(w, http.StatusBadRequest, "username query parameter is required")
		return
	}

	result, err := a.service.CheckUsernameAvailable(r.Context(), name)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, "could not check username availability")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), result)
}

// statusForProfileError maps the profile-patch failure modes onto status
// codes the client can branch on. Anything unrecognised is a 500 — a patch
// that fails for a reason we did not name is a server problem, and reporting
// it as 400 would tell the user to fix input that is not wrong.
func statusForProfileError(err error) int {
	var cooldown *UsernameCooldownError
	switch {
	case errors.Is(err, ErrUsernameInvalid),
		errors.Is(err, ErrSettingsInvalid),
		errors.Is(err, ErrSettingsTooLarge):
		return http.StatusBadRequest
	case errors.Is(err, ErrUsernameReserved), errors.Is(err, ErrUsernameTaken):
		return http.StatusConflict
	case errors.As(err, &cooldown):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}
