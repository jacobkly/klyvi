package imports

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

type API struct {
	service *Service
}

func NewAPI(service *Service) *API {
	return &API{service: service}
}

// Create — POST /v1/imports. Multipart: `source` + `file`. Returns the
// pending job immediately; the client polls Get for progress.
func (a *API) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes+64*1024)
	if err := r.ParseMultipartForm(maxImportBytes + 64*1024); err != nil {
		response.WriteError(w, http.StatusRequestEntityTooLarge, "import file too large")
		return
	}

	source := r.FormValue("source")
	if !ValidSource(source) {
		response.WriteError(w, http.StatusBadRequest, "source must be letterboxd, trakt, or csv")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "multipart field 'file' is required")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxImportBytes+1))
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "could not read upload")
		return
	}
	if len(data) > maxImportBytes {
		response.WriteError(w, http.StatusRequestEntityTooLarge, "import file too large")
		return
	}

	imp, err := a.service.Start(r.Context(), userID, source, data)
	switch {
	case errors.Is(err, ErrImportActive):
		response.WriteError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrTooManyRows), errors.Is(err, ErrExportTooLarge):
		response.WriteError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	case err != nil:
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.WriteSuccess(w, http.StatusAccepted, "v1",
		middleware.StatsFromContext(r.Context()), imp)
}

// Get — GET /v1/imports/{id}. Poll for status and match counts.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid import id")
		return
	}

	imp, err := a.service.Get(r.Context(), userID, id)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, "could not load import")
		return
	}
	if imp == nil {
		response.WriteError(w, http.StatusNotFound, "import not found")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), imp)
}
