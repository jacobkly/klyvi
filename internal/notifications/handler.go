package notifications

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

// API serves the notification endpoints.
type API struct {
	service *Service
}

// NewAPI builds an API on a Service.
func NewAPI(service *Service) *API {
	return &API{service: service}
}

// List handles GET /v1/notifications?limit=&offset=. A limit or offset that
// makes no sense falls back to the default rather than failing the page.
func (a *API) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	page, err := a.service.List(r.Context(), userID, limit, offset)
	if err != nil {
		internalError(w, err)
		return
	}
	response.WriteSuccess(w, http.StatusOK, "v1", middleware.StatsFromContext(r.Context()), page)
}

// markReadRequest names the notices to mark, or asks for all of them. Sending
// neither or both is refused, so an empty list can't be mistaken for "all".
type markReadRequest struct {
	IDs []int64 `json:"ids"`
	All bool    `json:"all"`
}

// MarkRead handles POST /v1/notifications/read.
func (a *API) MarkRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var req markReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.All == (len(req.IDs) > 0) {
		response.WriteError(w, http.StatusBadRequest, "send either ids or all")
		return
	}

	unread, err := a.mark(r, userID, req)
	if err != nil {
		internalError(w, err)
		return
	}
	response.WriteSuccess(w, http.StatusOK, "v1", middleware.StatsFromContext(r.Context()),
		map[string]int{"unread_count": unread})
}

func (a *API) mark(r *http.Request, userID uuid.UUID, req markReadRequest) (int, error) {
	if req.All {
		return a.service.MarkAllRead(r.Context(), userID)
	}
	return a.service.MarkRead(r.Context(), userID, req.IDs)
}

// internalError logs the detail and gives the client generic text, since a
// database error can name tables and columns.
func internalError(w http.ResponseWriter, err error) {
	log.Printf("notifications: %v", err)
	response.WriteError(w, http.StatusInternalServerError, "internal server error")
}
