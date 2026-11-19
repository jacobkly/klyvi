package errlog

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/platform/http/response"
)

const (
	adminPageSize    = 50
	adminMaxPageSize = 100
)

// groupStore is what the admin page needs from the store.
type groupStore interface {
	List(ctx context.Context, status Status, limit, offset int) ([]Group, error)
	Counts(ctx context.Context) (Counts, error)
	SetResolved(ctx context.Context, id int64, resolved bool) (Group, error)
}

// AdminAPI serves the admin error pages. The router puts it behind
// middleware.AdminOnly; nothing here checks who's asking.
type AdminAPI struct {
	store groupStore
}

// NewAdminAPI builds the admin handlers over the error store.
func NewAdminAPI(store groupStore) *AdminAPI {
	return &AdminAPI{store: store}
}

// ListPage is one page of error groups, with the totals for the tabs.
type ListPage struct {
	Errors  []Group `json:"errors"`
	Counts  Counts  `json:"counts"`
	HasMore bool    `json:"has_more"`
}

// List handles GET /v1/admin/errors?status=&limit=&offset=. status is open
// (the default), resolved or all. Paging that makes no sense falls back to
// the defaults.
func (a *AdminAPI) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := Status(q.Get("status"))
	switch status {
	case "":
		status = StatusOpen
	case StatusOpen, StatusResolved, StatusAll:
	default:
		response.WriteError(w, http.StatusBadRequest, "status must be open, resolved or all")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = adminPageSize
	}
	if limit > adminMaxPageSize {
		limit = adminMaxPageSize
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}

	groups, err := a.store.List(r.Context(), status, limit+1, offset)
	if err != nil {
		internalError(w, err)
		return
	}
	counts, err := a.store.Counts(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	page := ListPage{Errors: groups, Counts: counts}
	if len(groups) > limit {
		page.Errors, page.HasMore = groups[:limit], true
	}
	response.WriteSuccess(w, http.StatusOK, "v1", nil, page)
}

type updateRequest struct {
	Resolved *bool `json:"resolved"`
}

// Update handles PATCH /v1/admin/errors/{id} with {"resolved": true} to
// resolve a group or false to reopen it. It answers with the group.
func (a *AdminAPI) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid error id")
		return
	}
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Resolved == nil {
		response.WriteError(w, http.StatusBadRequest, "resolved is required")
		return
	}

	g, err := a.store.SetResolved(r.Context(), id, *req.Resolved)
	if errors.Is(err, ErrNotFound) {
		response.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	response.WriteSuccess(w, http.StatusOK, "v1", nil, g)
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("admin errors: %v", err)
	response.WriteError(w, http.StatusInternalServerError, "internal server error")
}
