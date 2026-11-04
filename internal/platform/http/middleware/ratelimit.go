package middleware

import (
	"math"
	"net/http"
	"net/netip"
	"strconv"
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

	// maxVisitors bounds the bucket map, so a client cycling through addresses
	// cannot grow it for as long as the TTL keeps entries alive. Well above what
	// one API instance sees from real clients within a TTL.
	maxVisitors = 50_000

	// ipv6BucketBits is how much of an IPv6 address identifies a client. One
	// customer usually holds a whole /64.
	ipv6BucketBits = 64
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
// cloudflared) and falls back to the TCP peer otherwise. IPv6 addresses share a
// bucket per /64, see bucketKey.
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

	table := newVisitorTable(limit, burst, maxVisitors)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Reserve (rather than Allow) so a rejection can report how long the
			// client should wait. A held token is cancelled on rejection so the
			// limiter state is unchanged, matching Allow's semantics.
			res := table.limiterFor(bucketKey(ClientIP(r)), time.Now()).Reserve()
			if !res.OK() {
				response.WriteError(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			if delay := res.Delay(); delay > 0 {
				res.Cancel()
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(delay.Seconds()))))
				response.WriteError(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bucketKey maps a client address to its rate limit bucket: a whole IPv4
// address, or the /64 of an IPv6 one. Anything unparseable keeps its own key.
func bucketKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(ipv6BucketBits)
	if err != nil {
		return ip
	}
	return prefix.String()
}

// visitorTable holds one token bucket per key, up to a fixed number of keys.
type visitorTable struct {
	mu        sync.Mutex
	visitors  map[string]*visitor
	lastSweep time.Time
	limit     rate.Limit
	burst     int
	max       int
}

func newVisitorTable(limit rate.Limit, burst, maxEntries int) *visitorTable {
	return &visitorTable{
		visitors:  make(map[string]*visitor),
		lastSweep: time.Now(),
		limit:     limit,
		burst:     burst,
		max:       maxEntries,
	}
}

// limiterFor returns the bucket for key, creating it if needed. When the table
// is full it drops an arbitrary entry rather than refusing the new client: the
// worst case is that someone gets a fresh bucket early, and a flood of new
// addresses cannot lock real users out. Dropping one entry is constant time, so
// a full table adds no per-request scan.
func (t *visitorTable) limiterFor(key string, now time.Time) *rate.Limiter {
	t.mu.Lock()
	defer t.mu.Unlock()

	if now.Sub(t.lastSweep) > sweepInterval {
		for k, v := range t.visitors {
			if now.Sub(v.lastSeen) > visitorTTL {
				delete(t.visitors, k)
			}
		}
		t.lastSweep = now
	}

	v, ok := t.visitors[key]
	if !ok {
		if len(t.visitors) >= t.max {
			for k := range t.visitors {
				delete(t.visitors, k)
				break
			}
		}
		v = &visitor{limiter: rate.NewLimiter(t.limit, t.burst)}
		t.visitors[key] = v
	}
	v.lastSeen = now
	return v.limiter
}
