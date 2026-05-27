package middleware

import (
	"log"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// UserLoggerMiddleware emits a one-line log per authenticated request that
// includes the user UUID alongside the request id. Mount it AFTER the auth
// middleware (which puts the UUID into context) on protected routes only.
//
// The base LoggerMiddleware already logs every request — this is a cheap
// addendum that makes "what did user X do" greppable without changing the
// base logger's output format for public routes.
func UserLoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := UserUUIDFromContext(r.Context()); ok {
			log.Printf("[USER] id=%s user=%s path=%s",
				chimw.GetReqID(r.Context()), id, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
