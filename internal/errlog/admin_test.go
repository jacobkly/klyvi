package errlog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeGroups struct {
	groups    []Group
	gotStatus Status
	gotLimit  int
	gotOffset int
	resolved  map[int64]bool
}

func (f *fakeGroups) List(_ context.Context, status Status, limit, offset int) ([]Group, error) {
	f.gotStatus, f.gotLimit, f.gotOffset = status, limit, offset
	if limit < len(f.groups) {
		return f.groups[:limit], nil
	}
	return f.groups, nil
}

func (f *fakeGroups) Counts(context.Context) (Counts, error) {
	return Counts{Open: 3, Resolved: 1}, nil
}

func (f *fakeGroups) SetResolved(_ context.Context, id int64, resolved bool) (Group, error) {
	if id != 7 {
		return Group{}, ErrNotFound
	}
	if f.resolved == nil {
		f.resolved = map[int64]bool{}
	}
	f.resolved[id] = resolved
	return Group{ID: id, Message: "boom"}, nil
}

func adminRouter(store *fakeGroups) http.Handler {
	api := NewAdminAPI(store)
	r := chi.NewRouter()
	r.Get("/v1/admin/errors", api.List)
	r.Patch("/v1/admin/errors/{id}", api.Update)
	return r
}

func TestAdminList(t *testing.T) {
	three := []Group{{ID: 3}, {ID: 2}, {ID: 1}}
	tests := []struct {
		name       string
		query      string
		wantCode   int
		wantStatus Status
		wantLimit  int // what the store is asked for: one more than the page
		wantOffset int
		wantMore   bool
	}{
		{"defaults to open, 50 a page", "", 200, StatusOpen, 51, 0, false},
		{"resolved", "?status=resolved", 200, StatusResolved, 51, 0, false},
		{"all, paged", "?status=all&limit=2&offset=4", 200, StatusAll, 3, 4, true},
		{"a limit past 100 is cut to 100", "?limit=500", 200, StatusOpen, 101, 0, false},
		{"nonsense paging uses the defaults", "?limit=x&offset=-3", 200, StatusOpen, 51, 0, false},
		{"an unknown status is refused", "?status=closed", 400, "", 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeGroups{groups: three}
			rec := httptest.NewRecorder()
			adminRouter(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/errors"+tc.query, nil))

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body)
			}
			if tc.wantCode != 200 {
				return
			}
			if store.gotStatus != tc.wantStatus || store.gotLimit != tc.wantLimit || store.gotOffset != tc.wantOffset {
				t.Errorf("store asked for %s/%d/%d, want %s/%d/%d",
					store.gotStatus, store.gotLimit, store.gotOffset, tc.wantStatus, tc.wantLimit, tc.wantOffset)
			}
			var got struct {
				Data struct {
					Errors  []Group `json:"errors"`
					Counts  Counts  `json:"counts"`
					HasMore bool    `json:"has_more"`
				} `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.Data.HasMore != tc.wantMore {
				t.Errorf("has_more = %v, want %v", got.Data.HasMore, tc.wantMore)
			}
			if tc.wantMore && len(got.Data.Errors) != 2 {
				t.Errorf("page has %d groups, want 2", len(got.Data.Errors))
			}
			if got.Data.Counts != (Counts{Open: 3, Resolved: 1}) {
				t.Errorf("counts = %+v", got.Data.Counts)
			}
		})
	}
}

func TestAdminUpdate(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		body         string
		wantCode     int
		wantResolved *bool
	}{
		{"resolves", "/v1/admin/errors/7", `{"resolved":true}`, 200, ptr(true)},
		{"reopens", "/v1/admin/errors/7", `{"resolved":false}`, 200, ptr(false)},
		{"an unknown group is 404", "/v1/admin/errors/8", `{"resolved":true}`, 404, nil},
		{"a bad id is 400", "/v1/admin/errors/x", `{"resolved":true}`, 400, nil},
		{"a body without resolved is 400", "/v1/admin/errors/7", `{}`, 400, nil},
		{"a broken body is 400", "/v1/admin/errors/7", `{`, 400, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeGroups{}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, tc.path, strings.NewReader(tc.body))
			adminRouter(store).ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body)
			}
			if tc.wantResolved != nil {
				if got, ok := store.resolved[7]; !ok || got != *tc.wantResolved {
					t.Errorf("store resolved = %v (%v), want %v", got, ok, *tc.wantResolved)
				}
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
