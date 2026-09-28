package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"klyvi-api/internal/platform/timing"
)

// queryTracer times every query and adds it to the request's timing.Recorder.
// Queries run without a request context (seeders, background jobs) have no
// Recorder and are skipped at no cost.
type queryTracer struct{}

type queryStartKey struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	if timing.FromContext(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, queryStartKey{}, time.Now())
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey{}).(time.Time)
	if !ok {
		return
	}
	timing.FromContext(ctx).AddDB(time.Since(start))
}
