package middleware

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"klyvi-api/internal/platform/http/response"
)

const (
	defaultRequestsPerMinute = 20
	defaultBurst             = 10

	// visitorTTL is how long an idle bucket is kept. Long enough that a user
	// filling in a form keeps their bucket, short enough that the map does
	// not grow with every IP that ever touched the endpoint.
	visitorTTL = 10 * time.Minute

	// sweepInterval is how often expired buckets are collected.
	sweepInterval = 5 * time.Minute
)

// RateLimitConfig tunes a RateLimit middleware. The zero value is usable and
// yields the package defaults.
type RateLimitConfig struct {
	// RequestsPerMinute is the sustained refill rate per client IP.
	RequestsPerMinute int
	// Burst is how many requests can be spent at once before the refill rate
	// starts to bite. Type-ahead arrives in bursts, so this needs headroom.
	Burst int
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimit caps requests per client IP using a token bucket.
//
// This exists for the public endpoints that read user data — an
// unauthenticated route over the users table is a free enumerator without a
// cap. It is deliberately in-process: a single API instance is the current
// deployment, and a shared store is only worth it once there is more than one.
//
// The bucket key comes from ClientIP, which reads the real client from
// CF-Connecting-IP behind the Cloudflare tunnel (where RemoteAddr is always
// cloudflared) and falls back to the TCP peer otherwise.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	perMinute := cfg.RequestsPerMinute
	if perMinute <= 0 {
		perMinute = defaultRequestsPerMinute
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = defaultBurst
	}
	limit := rate.Limit(float64(perMinute) / 60.0)

	var mu sync.Mutex
	visitors := make(map[string]*visitor)
	lastSweep := time.Now()

	limiterFor := func(key string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()

		now := time.Now()
		if now.Sub(lastSweep) > sweepInterval {
			for k, v := range visitors {
				if now.Sub(v.lastSeen) > visitorTTL {
					delete(visitors, k)
				}
			}
			lastSweep = now
		}

		v, ok := visitors[key]
		if !ok {
			v = &visitor{limiter: rate.NewLimiter(limit, burst)}
			visitors[key] = v
		}
		v.lastSeen = now
		return v.limiter
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiterFor(ClientIP(r)).Allow() {
				response.WriteError(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
