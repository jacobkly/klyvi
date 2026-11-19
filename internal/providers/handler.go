package providers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

// Getter looks up a title's offers in one country.
type Getter interface {
	Get(ctx context.Context, mediaType string, tmdbID int, region string) (*Result, error)
}

// API serves one media type's providers route.
type API struct {
	getter    Getter
	mediaType string
}

// NewAPI serves providers for "movie" or "tv" titles.
func NewAPI(getter Getter, mediaType string) *API {
	return &API{getter: getter, mediaType: mediaType}
}

var regionCode = regexp.MustCompile(`^[A-Z]{2}$`)

// Get serves GET /v1/{movies,tv}/{id}/providers?region=GB (public): where to
// stream, rent or buy the title in one country, US by default.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id <= 0 {
		response.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	region := strings.ToUpper(r.URL.Query().Get("region"))
	if region == "" {
		region = DefaultRegion
	}
	if !regionCode.MatchString(region) {
		response.WriteError(w, http.StatusBadRequest, "region must be a two-letter country code")
		return
	}
	res, err := a.getter.Get(r.Context(), a.mediaType, id, region)
	if errors.Is(err, ErrNotFound) {
		response.WriteError(w, http.StatusNotFound, "title not found")
		return
	}
	if err != nil {
		log.Printf("providers: %v", err)
		response.WriteError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	response.WriteSuccess(w, http.StatusOK, "v1",
		middleware.StatsFromContext(r.Context()), res)
}
