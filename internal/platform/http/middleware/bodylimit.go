package middleware

import (
	"net/http"

	"klyvi-api/internal/platform/http/response"
)

// MaxBodyBytes rejects requests whose body exceeds n bytes. A declared
// Content-Length over the limit is refused up front with 413; the body is also
// wrapped in http.MaxBytesReader as a backstop for chunked or under-declared
// bodies, which then surface as the handler's normal decode error.
//
// Apply it only to routes that decode a JSON body — multipart uploads (avatar,
// imports) cap themselves and must not be double-limited here.
func MaxBodyBytes(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				response.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
