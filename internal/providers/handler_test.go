package providers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/providers"
)

type fakeGetter struct {
	gotType   string
	gotID     int
	gotRegion string
	err       error
}

func (f *fakeGetter) Get(_ context.Context, mediaType string, id int, region string) (*providers.Result, error) {
	f.gotType, f.gotID, f.gotRegion = mediaType, id, region
	if f.err != nil {
		return nil, f.err
	}
	return &providers.Result{Region: region}, nil
}

func serve(g *fakeGetter, mediaType, path string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/{id}/providers", providers.NewAPI(g, mediaType).Get)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestProvidersHandler(t *testing.T) {
	cases := []struct {
		path   string
		status int
		region string
	}{
		{"/95396/providers", http.StatusOK, "US"},
		{"/95396/providers?region=gb", http.StatusOK, "GB"},
		{"/95396/providers?region=GBR", http.StatusBadRequest, ""},
		{"/95396/providers?region=1A", http.StatusBadRequest, ""},
		{"/0/providers", http.StatusBadRequest, ""},
		{"/x/providers", http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		g := &fakeGetter{}
		rec := serve(g, "tv", tc.path)
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, rec.Code, tc.status)
		}
		if tc.status == http.StatusOK && (g.gotRegion != tc.region || g.gotType != "tv" || g.gotID != 95396) {
			t.Errorf("%s: asked for %s %d %q", tc.path, g.gotType, g.gotID, g.gotRegion)
		}
	}

	if rec := serve(&fakeGetter{err: providers.ErrNotFound}, "movie", "/9/providers"); rec.Code != http.StatusNotFound {
		t.Errorf("not found: status %d, want 404", rec.Code)
	}
}
