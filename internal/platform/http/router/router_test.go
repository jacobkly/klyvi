package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"klyvi-api/internal/onboarding"
	"klyvi-api/internal/platform/http/router"
	"klyvi-api/internal/users"
)

type emptyPool struct{}

func (emptyPool) ListActiveWithCatalog(context.Context, int) ([]onboarding.PoolRow, error) {
	return nil, nil
}

// The onboarding pool is the same curated deck for every visitor, so browsers
// and the CDN may keep it like the rest of the public catalog.
func TestOnboardingPoolIsCacheable(t *testing.T) {
	r := router.New(router.Services{
		Users:      users.NewService(nil),
		Onboarding: onboarding.NewService(emptyPool{}, nil),
		AuthMW:     func(next http.Handler) http.Handler { return next },
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/onboarding/pool", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Header().Get("Cache-Control"), "public, max-age=3600, stale-while-revalidate=86400"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
}
