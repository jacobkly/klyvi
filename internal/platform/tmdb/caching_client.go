package tmdb

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"klyvi-api/internal/platform/cache"
)

// requester is the upstream this decorator wraps. The real *Client satisfies it.
type requester interface {
	TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// Cache tuning by content class. Catalog data moves slowly; search is the least
// cacheable so it gets a shorter window and its own size cap.
const (
	searchCacheTTL   = 6 * time.Hour
	listsCacheTTL    = 12 * time.Hour
	longCacheTTL     = 24 * time.Hour
	searchCacheSize  = 1024
	catalogCacheSize = 2048
)

// CachingTMDBClient caches GET responses from an upstream TMDB client. Only GET
// is cached: TMDB requests are user-agnostic, so the endpoint string is a
// complete, safe cache key. Non-GET methods pass straight through.
//
// A singleflight group collapses concurrent misses for the same endpoint into a
// single upstream call, so a cold key under load costs one request, not many.
// That call carries the context of the caller that started it, so a search the
// user abandoned stops at TMDB instead of finishing for nobody.
//
// Cached values are the decoded response maps, treated as read-only: every hit
// returns the same map, so callers must not mutate it. Current consumers
// (movies, tv, search, rankings, imports) only read.
type CachingTMDBClient struct {
	upstream requester
	search   *cache.TTL[map[string]interface{}]
	lists    *cache.TTL[map[string]interface{}]
	long     *cache.TTL[map[string]interface{}]
	group    singleflight.Group
}

// NewCachingTMDBClient wraps upstream with the per-class caches.
func NewCachingTMDBClient(upstream requester) *CachingTMDBClient {
	return &CachingTMDBClient{
		upstream: upstream,
		search:   cache.NewTTL[map[string]interface{}](searchCacheSize, searchCacheTTL),
		lists:    cache.NewTTL[map[string]interface{}](catalogCacheSize, listsCacheTTL),
		long:     cache.NewTTL[map[string]interface{}](catalogCacheSize, longCacheTTL),
	}
}

func (c *CachingTMDBClient) TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error) {
	if method != http.MethodGet {
		return c.upstream.TMDBRequest(ctx, method, endpoint, body)
	}

	bucket := c.bucketFor(endpoint)
	if v, ok := bucket.Get(endpoint); ok {
		return v, nil
	}

	v, err, _ := c.group.Do(endpoint, func() (interface{}, error) {
		// Re-check under the singleflight leader: a request that missed the
		// outer Get may reach here just after another leader filled the cache.
		if hit, ok := bucket.Get(endpoint); ok {
			return hit, nil
		}
		res, err := c.upstream.TMDBRequest(ctx, method, endpoint, body)
		if err != nil {
			return nil, err // never cache errors
		}
		bucket.Set(endpoint, res)
		return res, nil
	})
	if err != nil {
		// The fetch runs on its first caller's context. If that request went
		// away mid-fetch, this one is still live and starts over.
		if isContextErr(err) && ctx.Err() == nil {
			return c.TMDBRequest(ctx, method, endpoint, body)
		}
		return nil, err
	}
	return v.(map[string]interface{}), nil
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// bucketFor routes an endpoint to its cache class. Anything unrecognised falls
// to the long (24h) bucket, which is a safe default for stable catalog reads.
func (c *CachingTMDBClient) bucketFor(endpoint string) *cache.TTL[map[string]interface{}] {
	switch {
	case strings.Contains(endpoint, "/search/"):
		return c.search
	case isListEndpoint(endpoint):
		return c.lists
	default:
		return c.long
	}
}

// isListEndpoint matches the browse feeds (trending + the movie/tv popular /
// upcoming / top_rated style lists), which refresh a little faster than detail.
func isListEndpoint(endpoint string) bool {
	return strings.Contains(endpoint, "/trending/") ||
		strings.Contains(endpoint, "/movie/popular") ||
		strings.Contains(endpoint, "/movie/upcoming") ||
		strings.Contains(endpoint, "/movie/top_rated") ||
		strings.Contains(endpoint, "/tv/popular") ||
		strings.Contains(endpoint, "/tv/top_rated") ||
		strings.Contains(endpoint, "/tv/on_the_air") ||
		strings.Contains(endpoint, "/tv/airing_today")
}
