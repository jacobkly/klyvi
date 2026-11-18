package middleware

import (
	"log"
	"net/http"
	"time"
)

// LongRequest gives one route d to read its body and write its response,
// in place of the server-wide timeouts. Those are a few seconds and cover the
// whole body, which is right for JSON but fails a 10 MB import on a slow
// phone or a large export. The deadline is set per request, so the short
// server defaults still protect every other route.
func LongRequest(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := http.NewResponseController(w)
			deadline := time.Now().Add(d)
			if err := rc.SetReadDeadline(deadline); err != nil {
				log.Printf("long request %s: could not extend the read deadline: %v", r.URL.Path, err)
			}
			if err := rc.SetWriteDeadline(deadline); err != nil {
				log.Printf("long request %s: could not extend the write deadline: %v", r.URL.Path, err)
			}
			next.ServeHTTP(w, r)
		})
	}
}
