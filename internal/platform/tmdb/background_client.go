package tmdb

import (
	"context"

	"golang.org/x/time/rate"
)

// Background work (imports) gets this slice of the shared TMDB budget. It sits
// well under tmdbRatePerSec, so however hard background jobs run, the shared
// limiter keeps refilling and a page request finds a token waiting.
const (
	backgroundRatePerSec = 15
	backgroundBurst      = 5
)

// BackgroundClient caps background callers at their own rate before they
// reach the shared client. Page requests skip it and go straight to the
// shared client, so they come first.
type BackgroundClient struct {
	upstream requester
	limiter  *rate.Limiter
}

// NewBackgroundClient wraps the shared upstream with the background share.
func NewBackgroundClient(upstream requester) *BackgroundClient {
	return &BackgroundClient{
		upstream: upstream,
		limiter:  rate.NewLimiter(rate.Limit(backgroundRatePerSec), backgroundBurst),
	}
}

func (b *BackgroundClient) TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error) {
	// Same contract as Client: a caller that went away gets ctx's error.
	if err := b.limiter.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return b.upstream.TMDBRequest(ctx, method, endpoint, body)
}
