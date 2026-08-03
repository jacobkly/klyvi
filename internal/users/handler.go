package users

import (
	"encoding/json"
	"errors"
	"io"
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

// UploadAvatar — POST /v1/users/me/avatar. Multipart field `file`.
//
// The client never talks to storage directly (architecture rule): the API
// receives the image, normalises it (512px square JPEG, EXIF stripped by
// re-encode), stores it under the user id, and writes the public URL to the
// profile row.
func (a *API) UploadAvatar(w http.ResponseWriter, r *http.Request) {
	id, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	// Cap the whole request slightly above the image cap so a too-large
	// image reaches the typed 413 below instead of a socket error.
	r.Body = http.MaxBytesReader(w, r.Body, MaxAvatarBytes+512*1024)
	if err := r.ParseMultipartForm(MaxAvatarBytes + 512*1024); err != nil {
		response.WriteError(w, http.StatusRequestEntityTooLarge, ErrImageTooLarge.Error())
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "multipart field 'file' is required")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxAvatarBytes+1))
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "could not read upload")
		return
	}
	if len(data) > MaxAvatarBytes {
		response.WriteError(w, http.StatusRequestEntityTooLarge, ErrImageTooLarge.Error())
		return
	}

	user, err := a.service.UpdateAvatar(r.Context(), id, data)
	if err != nil {
		response.WriteError(w, statusForAvatarError(err), err.Error())
		return
	}
	if user == nil {
		response.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), user)
}

// DeleteAvatar — DELETE /v1/users/me/avatar.
func (a *API) DeleteAvatar(w http.ResponseWriter, r *http.Request) {
	id, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	user, err := a.service.DeleteAvatar(r.Context(), id)
	if err != nil {
		response.WriteError(w, statusForAvatarError(err), err.Error())
		return
	}
	if user == nil {
		response.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), user)
}

func statusForAvatarError(err error) int {
	switch {
	case errors.Is(err, ErrNotAnImage):
		return http.StatusBadRequest
	case errors.Is(err, ErrImageTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		// Includes ErrStorageNotConfigured — a deployment problem, not the
		// caller's input.
		return http.StatusInternalServerError
	}
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
		errors.Is(err, ErrSettingsTooLarge),
		errors.Is(err, ErrBirthdayInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrUsernameReserved), errors.Is(err, ErrUsernameTaken):
		return http.StatusConflict
	case errors.As(err, &cooldown):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}
