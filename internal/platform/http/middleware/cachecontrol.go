package middleware

import (
	"net/http"
	"strconv"
)

// CacheControl marks successful responses cacheable with the given max-age and
// stale-while-revalidate (seconds; swr omitted when 0). Only 2xx responses get
// the header, so a transient upstream error is never cached at the browser or
// CDN. Apply it only to public catalog reads — never to per-user routes.
func CacheControl(maxAge, swr int) func(http.Handler) http.Handler {
	value := "public, max-age=" + strconv.Itoa(maxAge)
	if swr > 0 {
		value += ", stale-while-revalidate=" + strconv.Itoa(swr)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(&cacheControlWriter{ResponseWriter: w, value: value}, r)
		})
	}
}

// cacheControlWriter sets the Cache-Control header just before the status is
// written, and only for 2xx. Setting it here (not up front) keeps error
// responses uncacheable.
type cacheControlWriter struct {
	http.ResponseWriter
	value       string
	wroteHeader bool
}

func (w *cacheControlWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if status >= 200 && status < 300 {
			w.Header().Set("Cache-Control", w.value)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *cacheControlWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK) // mirror net/http's implicit 200
	}
	return w.ResponseWriter.Write(b)
}
