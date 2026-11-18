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
		response.WriteError(w, http.StatusBadRequest, "invalid json")
		return
	}

	entry, err := a.service.Add(r.Context(), userID, req)
	if err != nil {
		respondError(w, err)
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
		response.WriteError(w, http.StatusBadRequest, "invalid json")
		return
	}

	entry, err := a.service.Update(r.Context(), userID, mediaID, req)
	if err != nil {
		respondError(w, err)
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
		respondError(w, err)
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

// scopeRequest is the body for the danger-zone operations: a single
// media-type scope, required.
type scopeRequest struct {
	MediaType string `json:"media_type"`
}

// ResetScores — POST /v1/tracking/reset-scores. Nulls every score in the
// given media-type scope. Irreversible; the client confirms intent.
func (a *API) ResetScores(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var req scopeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid json")
		return
	}

	affected, err := a.service.ResetScores(r.Context(), userID, req.MediaType)
	if err != nil {
		respondError(w, err)
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()),
		map[string]int64{"affected": affected})
}

// DeleteList — DELETE /v1/tracking. Deletes every row in the given
// media-type scope. Irreversible.
func (a *API) DeleteList(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var req scopeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid json")
		return
	}

	affected, err := a.service.DeleteList(r.Context(), userID, req.MediaType)
	if err != nil {
		respondError(w, err)
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()),
		map[string]int64{"affected": affected})
}

// Lookup — GET /v1/tracking/lookup?tmdb_id=&media_type=&season_number=.
// Returns the user's entry for one title, or 404 when they do not track it.
func (a *API) Lookup(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	q := r.URL.Query()
	tmdbID, err := strconv.Atoi(q.Get("tmdb_id"))
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid tmdb_id")
		return
	}
	req := LookupRequest{TMDBID: tmdbID, MediaType: q.Get("media_type")}
	if v := q.Get("season_number"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			response.WriteError(w, http.StatusBadRequest, "invalid season_number")
			return
		}
		req.SeasonNumber = &n
	}

	entry, err := a.service.Lookup(r.Context(), userID, req)
	if err != nil {
		respondError(w, err)
		return
	}
	if entry == nil {
		response.WriteError(w, http.StatusNotFound, "tracking entry not found")
		return
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), entry)
}

// List — GET /v1/tracking?media_type=movie&status=completed&limit=100&cursor=...
// With no limit it returns the whole list.
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
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			response.WriteError(w, http.StatusBadRequest, "limit must be at least 1")
			return
		}
		filters.Limit = n
	}
	if v := q.Get("cursor"); v != "" {
		c, err := ParseCursor(v)
		if err != nil {
			respondError(w, err)
			return
		}
		filters.After = c
	}

	entries, err := a.service.List(r.Context(), userID, filters)
	if err != nil {
		respondError(w, err)
		return
	}
	// Guarantee a JSON array (not null) for callers; OpenAPI declares the
	// field as an array.
	if entries == nil {
		entries = []Entry{}
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), entries)
}
