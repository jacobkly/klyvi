package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
)

func TestAdminOnly(t *testing.T) {
	admin := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	other := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	// What chi answers for a route that doesn't exist.
	unknown := httptest.NewRecorder()
	http.NotFound(unknown, httptest.NewRequest(http.MethodGet, "/v1/nope", nil))

	tests := []struct {
		name     string
		user     *uuid.UUID
		wantCode int
	}{
		{"an admin gets through", &admin, http.StatusOK},
		{"another user gets the unknown-route 404", &other, http.StatusNotFound},
		{"a signed-out visitor gets the unknown-route 404", nil, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			h := middleware.AdminOnly([]uuid.UUID{admin})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/v1/admin/errors", nil)
			if tc.user != nil {
				req = req.WithContext(middleware.WithUserUUID(req.Context(), *tc.user))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantCode == http.StatusOK {
				if !reached {
					t.Fatal("the admin never reached the handler")
				}
				if got := rec.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", got)
				}
				return
			}
			if reached {
				t.Fatal("a non-admin reached the handler")
			}
			if rec.Body.String() != unknown.Body.String() || rec.Header().Get("Content-Type") != unknown.Header().Get("Content-Type") {
				t.Errorf("404 = %q (%s), want the unknown-route one %q (%s)",
					rec.Body.String(), rec.Header().Get("Content-Type"),
					unknown.Body.String(), unknown.Header().Get("Content-Type"))
			}
		})
	}
}

func TestParseUserIDs(t *testing.T) {
	a := "11111111-1111-1111-1111-111111111111"
	b := "22222222-2222-2222-2222-222222222222"

	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"empty is no admins", "", 0, false},
		{"one", a, 1, false},
		{"several, with spaces and a trailing comma", " " + a + " , " + b + ",", 2, false},
		{"a typo fails loudly", a + ",not-a-uuid", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := middleware.ParseUserIDs(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d ids, want %d", len(got), tc.want)
			}
		})
	}
}
