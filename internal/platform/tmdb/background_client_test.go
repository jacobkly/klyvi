package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// pageWaitDuringBackgroundLoad keeps several background callers hammering
// TMDB through bg while page requests go straight to the shared client, and
// returns the slowest page request.
func pageWaitDuringBackgroundLoad(t *testing.T, shared *Client, bg requester) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_, _ = bg.TMDBRequest(ctx, "GET", "/search/movie?query=x", nil)
			}
		}()
	}
	defer func() {
		cancel()
		wg.Wait()
	}()

	// Let the background load use up any starting burst first.
	time.Sleep(500 * time.Millisecond)

	var slowest time.Duration
	for range 10 {
		start := time.Now()
		if _, err := shared.TMDBRequest(context.Background(), "GET", "/movie/1", nil); err != nil {
			t.Fatalf("page request: %v", err)
		}
		slowest = max(slowest, time.Since(start))
		time.Sleep(50 * time.Millisecond)
	}
	return slowest
}

// A big import is background work. Its share of the TMDB budget is capped
// below the shared limit, so a page request during the import still finds a
// token waiting instead of queueing behind the import's calls.
func TestBackgroundClient_PageRequestsKeepTheirShare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	shared := newClientForTest("token", srv.URL, tmdbRatePerSec, tmdbBurst)
	slowest := pageWaitDuringBackgroundLoad(t, shared, NewBackgroundClient(shared))

	if slowest > 25*time.Millisecond {
		t.Errorf("slowest page request took %v during background load, want under 25ms", slowest)
	}
}
