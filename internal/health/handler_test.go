package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"klyvi-api/internal/health"
)

type fakePinger struct {
	err      error
	deadline time.Time
}

func (p *fakePinger) PingContext(ctx context.Context) error {
	p.deadline, _ = ctx.Deadline()
	return p.err
}

// blockingPinger stands in for a database that never answers.
type blockingPinger struct{}

func (blockingPinger) PingContext(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestReady(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"database up", nil, http.StatusOK},
		{"database down", errors.New("connection refused"), http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePinger{err: tt.err}
			rec := httptest.NewRecorder()
			health.Ready(p).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
			if p.deadline.IsZero() {
				t.Fatal("the ping ran with no time limit")
			}
		})
	}
}

func TestReady_ADatabaseThatHangsIsDownWithinTheLimit(t *testing.T) {
	start := time.Now()
	rec := httptest.NewRecorder()
	health.Ready(blockingPinger{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	if took := time.Since(start); took > health.ReadyTimeout+time.Second {
		t.Fatalf("took %s, want about %s", took, health.ReadyTimeout)
	}
}
