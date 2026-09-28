package router_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
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

// JSON goes out gzipped to a client that accepts it, and plain to one that
// does not. Detail payloads are mostly repetitive JSON keys, so this is a
// large cut in bytes on the wire.
func TestJSONIsGzippedWhenAccepted(t *testing.T) {
	r := router.New(router.Services{
		Users:      users.NewService(nil),
		Onboarding: onboarding.NewService(emptyPool{}, nil),
		AuthMW:     func(next http.Handler) http.Handler { return next },
	})

	tests := []struct {
		name           string
		acceptEncoding string
		wantEncoding   string
	}{
		{"gzip accepted", "gzip, deflate, br", "gzip"},
		{"no encoding accepted", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/pool", nil)
			if tt.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Fatalf("Content-Encoding = %q, want %q", got, tt.wantEncoding)
			}

			var body io.Reader = rec.Body
			if tt.wantEncoding == "gzip" {
				zr, err := gzip.NewReader(rec.Body)
				if err != nil {
					t.Fatalf("gzip reader: %v", err)
				}
				body = zr
			}
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			if err := json.NewDecoder(body).Decode(&envelope); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if envelope.Data == nil {
				t.Error("response has no data field")
			}
		})
	}
}
