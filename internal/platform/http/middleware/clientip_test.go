package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"klyvi-api/internal/platform/http/middleware"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		cfHeader   string
		xff        string
		remoteAddr string
		want       string
	}{
		{
			name:       "prefers CF-Connecting-IP over the tunnel peer",
			cfHeader:   "203.0.113.7",
			remoteAddr: "172.18.0.5:4001", // cloudflared on the docker network
			want:       "203.0.113.7",
		},
		{
			name:       "ignores a forged X-Forwarded-For when the CF header is present",
			cfHeader:   "203.0.113.7",
			xff:        "1.2.3.4",
			remoteAddr: "172.18.0.5:4001",
			want:       "203.0.113.7",
		},
		{
			// Security: X-Forwarded-For's leading entries are attacker-controlled
			// (Cloudflare appends, it does not strip). It must never be trusted as
			// a rate-limit key, or one client could mint unlimited buckets.
			name:       "never trusts X-Forwarded-For as a fallback",
			xff:        "1.2.3.4",
			remoteAddr: "198.51.100.9:5000",
			want:       "198.51.100.9",
		},
		{
			name:       "falls back to the RemoteAddr host when no CF header",
			remoteAddr: "198.51.100.9:5000",
			want:       "198.51.100.9",
		},
		{
			name:       "returns RemoteAddr unchanged when it carries no port",
			remoteAddr: "198.51.100.9",
			want:       "198.51.100.9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.cfHeader != "" {
				req.Header.Set("CF-Connecting-IP", tt.cfHeader)
			}
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := middleware.ClientIP(req); got != tt.want {
				t.Errorf("ClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
