package tracking_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/tracking"
)

// A collection band asks for the user's rows for every film in it at once.
func TestTracking_List_FiltersByTMDBIDs(t *testing.T) {
	env := setupTracking(t)
	api := tracking.NewAPI(env.service)
	trackFive(t, env)

	rows := listPage(t, api, env, url.Values{"media_type": {"movie"}, "tmdb_ids": {"550,13,999999"}})
	var got []int
	for _, e := range rows {
		got = append(got, *e.TMDBID)
	}
	slices.Sort(got)
	if want := []int{13, 550}; !slices.Equal(got, want) {
		t.Errorf("tmdb_ids=550,13,999999: got %v, want %v", got, want)
	}
}

func TestTracking_List_RejectsBadTMDBIDs(t *testing.T) {
	env := setupTracking(t)
	api := tracking.NewAPI(env.service)

	many := make([]string, tracking.MaxListTMDBIDs+1)
	for i := range many {
		many[i] = strconv.Itoa(i + 1)
	}
	for _, v := range []string{"x", "550,", "0", "-3", strings.Join(many, ",")} {
		req := httptest.NewRequest("GET", "/v1/tracking?"+url.Values{"tmdb_ids": {v}}.Encode(), nil)
		req = req.WithContext(middleware.WithUserUUID(req.Context(), env.userID))
		rec := httptest.NewRecorder()
		api.List(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("tmdb_ids=%.20s: status %d, want 400", v, rec.Code)
		}
	}
}
