package family

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

// Finder looks up a title's family.
type Finder interface {
	Family(ctx context.Context, mediaType string, tmdbID int) ([]Title, error)
}

// API serves one media type's family route.
type API struct {
	finder    Finder
	mediaType string
}

// NewAPI serves families for "movie" or "tv" titles.
func NewAPI(finder Finder, mediaType string) *API {
	return &API{finder: finder, mediaType: mediaType}
}

// Get serves GET /v1/{movies,tv}/{id}/family (public): the titles that share
// a series or franchise with this one, oldest first. Empty when none do.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id <= 0 {
		response.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	titles, err := a.finder.Family(r.Context(), a.mediaType, id)
	if err != nil {
		log.Printf("family: %v", err)
		response.WriteError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if titles == nil {
		titles = []Title{}
	}
	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), titles)
}
