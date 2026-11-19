package middleware

import (
	"log"
	"net/http"
	"runtime/debug"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"klyvi-api/internal/errlog"
	"klyvi-api/internal/platform/http/response"
)

// Recoverer turns a panic in any downstream handler into a logged 500 rather
// than a dropped connection. http.ErrAbortHandler is re-panicked untouched — the
// net/http server uses it to intentionally abort a response, and swallowing it
// would break that contract.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			stack := debug.Stack()
			log.Printf("panic recovered [%s]: %v\n%s",
				chimiddleware.GetReqID(r.Context()), rec, stack)
			errlog.RecordPanic(r, rec, stack)
			response.WriteError(w, http.StatusInternalServerError, "internal server error")
		}()
		next.ServeHTTP(w, r)
	})
}
