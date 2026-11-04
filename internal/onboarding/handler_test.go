package onboarding_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"klyvi-api/internal/onboarding"
)

type failingPool struct{}

func (failingPool) ListActiveWithCatalog(context.Context, int) ([]onboarding.PoolRow, error) {
	return nil, errors.New("pq: relation onboarding_pool does not exist")
}

func TestPool_HidesDatabaseErrors(t *testing.T) {
	api := onboarding.NewAPI(onboarding.NewService(failingPool{}, &fakeCatalog{}))

	rec := httptest.NewRecorder()
	api.Pool(rec, httptest.NewRequest(http.MethodGet, "/v1/onboarding/pool", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Error != "internal server error" {
		t.Errorf("error = %q, want the generic message", body.Error)
	}
}
