package reco

import (
	"log"
	"net/http"
	"strconv"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

// Feed pagination defaults and bounds. Callers page with ?limit=&offset=;
// a page shorter than limit (or empty) signals the end of the feed.
const (
	defaultFeedLimit = 30
	maxFeedLimit     = 50
)

type API struct {
	orch *Orchestrator
}

func NewAPI(orch *Orchestrator) *API {
	return &API{orch: orch}
}

// Feed — GET /v1/reco/feed. Returns the top-K recommendations for the
// authenticated user. UUID is read from request context (placed there by
// the JWT middleware); no user id is accepted from the request.
func (a *API) Feed(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserUUIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), defaultFeedLimit)
	offset := atoiDefault(q.Get("offset"), 0)
	if limit < 1 {
		limit = defaultFeedLimit
	}
	if limit > maxFeedLimit {
		limit = maxFeedLimit
	}
	if offset < 0 {
		offset = 0
	}

	scored, err := a.orch.Feed(r.Context(), userID, limit, offset)
	if err != nil {
		log.Printf("reco: %v", err)
		response.WriteError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// Guarantee a JSON array, never null, matching the documented shape.
	if scored == nil {
		scored = []Scored{}
	}

	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), scored)
}

// atoiDefault parses s as an int, returning def when s is empty or invalid.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
