package tracking_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/tracking"
)

// listPage calls GET /v1/tracking with the given query and returns the rows.
func listPage(t *testing.T, api *tracking.API, env *trackingTestEnv, q url.Values) []tracking.Entry {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/tracking?"+q.Encode(), nil)
	req = req.WithContext(middleware.WithUserUUID(req.Context(), env.userID))
	rec := httptest.NewRecorder()
	api.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d body %s", q.Encode(), rec.Code, rec.Body.String())
	}
	var body struct {
		Data []tracking.Entry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Data
}

// cursorAfter is the cursor a client builds from the last row of a page.
func cursorAfter(e tracking.Entry) string {
	return e.UpdatedAt.Format(time.RFC3339Nano) + "," + strconv.Itoa(e.ID)
}

// trackFive adds five films and gives three of them the same updated_at, so
// the pages have to break the tie on id to join up.
func trackFive(t *testing.T, env *trackingTestEnv) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, id := range []int{550, 13, 680, 155, 27205} {
		if _, err := env.service.Add(ctx, env.userID, tracking.AddRequest{
			TMDBID:    id,
			MediaType: tracking.MediaTypeMovie,
		}); err != nil {
			t.Fatalf("Add %d: %v", id, err)
		}
	}
	if _, err := env.db.ExecContext(ctx, `
		update media_list set updated_at = '2026-01-01T00:00:00.123456Z'
		where user_id = $1 and id in (
			select id from media_list where user_id = $1 order by id limit 3
		)`, env.userID); err != nil {
		t.Fatalf("tie updated_at: %v", err)
	}
}

func TestTracking_List_PagesJoinUpWithNoGapsOrRepeats(t *testing.T) {
	env := setupTracking(t)
	api := tracking.NewAPI(env.service)
	trackFive(t, env)

	whole := listPage(t, api, env, url.Values{})
	if len(whole) != 5 {
		t.Fatalf("whole list: got %d rows, want 5", len(whole))
	}

	var paged []tracking.Entry
	q := url.Values{"limit": {"2"}}
	for i := 0; i < 5; i++ {
		page := listPage(t, api, env, q)
		if len(page) > 2 {
			t.Fatalf("page %d has %d rows, limit was 2", i, len(page))
		}
		paged = append(paged, page...)
		if len(page) < 2 {
			break
		}
		q.Set("cursor", cursorAfter(page[len(page)-1]))
	}

	if len(paged) != len(whole) {
		t.Fatalf("paged %d rows, whole list has %d", len(paged), len(whole))
	}
	for i := range whole {
		if paged[i].ID != whole[i].ID {
			t.Errorf("row %d: paged id %d, whole list id %d", i, paged[i].ID, whole[i].ID)
		}
	}
}

// A row the user edits after seeing it jumps to the top. It must not come
// round again on a later page.
func TestTracking_List_AnEditedRowDoesNotRepeat(t *testing.T) {
	env := setupTracking(t)
	api := tracking.NewAPI(env.service)
	trackFive(t, env)

	first := listPage(t, api, env, url.Values{"limit": {"2"}})
	if len(first) != 2 {
		t.Fatalf("first page: got %d rows", len(first))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	score := 80
	if _, err := env.service.Update(ctx, env.userID, first[1].MediaID, tracking.UpdateRequest{Score: &score}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	rest := listPage(t, api, env, url.Values{"cursor": {cursorAfter(first[1])}})
	if len(rest) != 3 {
		t.Fatalf("rest: got %d rows, want 3", len(rest))
	}
	for _, e := range rest {
		if e.ID == first[0].ID || e.ID == first[1].ID {
			t.Errorf("row %d came round again", e.ID)
		}
	}
}

func TestTracking_List_RejectsABadLimitOrCursor(t *testing.T) {
	env := setupTracking(t)
	api := tracking.NewAPI(env.service)

	for _, q := range []string{"limit=0", "limit=x", "cursor=nope", "cursor=2026-01-01T00:00:00Z,x"} {
		req := httptest.NewRequest("GET", "/v1/tracking?"+q, nil)
		req = req.WithContext(middleware.WithUserUUID(req.Context(), env.userID))
		rec := httptest.NewRecorder()
		api.List(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
}

func TestTracking_List_CutsALargeLimit(t *testing.T) {
	env := setupTracking(t)
	api := tracking.NewAPI(env.service)
	trackFive(t, env)

	if got := len(listPage(t, api, env, url.Values{"limit": {"100000"}})); got != 5 {
		t.Errorf("limit=100000: got %d rows, want 5", got)
	}
}
