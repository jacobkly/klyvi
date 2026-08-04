package middleware

import (
	"net"
	"net/http"
)

// ClientIP returns the caller's real IP, for rate-limiting keys and logging.
//
// The origin is reachable only through the Cloudflare tunnel, so the TCP peer
// (RemoteAddr) is always cloudflared and useless as a per-client key. Cloudflare
// sets CF-Connecting-IP to the edge-observed client IP and overwrites any copy
// the client tries to send, so it is safe to trust in this topology.
//
// X-Forwarded-For is deliberately NOT trusted: Cloudflare appends to it, so its
// leading entries are attacker-controlled. Keying a rate limiter on a forgeable
// value would let one client mint unlimited buckets and bypass the limit. When a
// request arrives without CF-Connecting-IP (i.e. not through the tunnel) we fall
// back to the unforgeable TCP peer address instead.
//
// This trust holds only while the origin stays tunnel-only. If a host port is
// ever published, CF-Connecting-IP becomes client-settable and this must change.
func ClientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
