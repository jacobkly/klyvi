package family_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/family"
)

type fakeFinder struct {
	gotType string
	gotID   int
	titles  []family.Title
}

func (f *fakeFinder) Family(_ context.Context, mediaType string, id int) ([]family.Title, error) {
	f.gotType, f.gotID = mediaType, id
	return f.titles, nil
}

func serve(t *testing.T, finder *fakeFinder, mediaType, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/{id}/family", family.NewAPI(finder, mediaType).Get)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestFamilyHandler(t *testing.T) {
	f := &fakeFinder{titles: []family.Title{}}
	rec := serve(t, f, "tv", "/1396/family")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if f.gotType != "tv" || f.gotID != 1396 {
		t.Errorf("asked for %s %d, want tv 1396", f.gotType, f.gotID)
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("body %s, want an empty data list", rec.Body.String())
	}

	for _, path := range []string{"/x/family", "/0/family", "/-4/family"} {
		if rec := serve(t, &fakeFinder{}, "movie", path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, rec.Code)
		}
	}
}
