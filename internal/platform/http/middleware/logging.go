package middleware

import (
	"log"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"klyvi-api/internal/platform/timing"
)

type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	start       time.Time
	rec         *timing.Recorder
}

// WriteHeader stamps Server-Timing with the DB and TMDB work done so far, the
// last moment a header can still be added.
func (rw *responseWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.wroteHeader = true
		rw.status = code
		rw.Header().Set("Server-Timing", rw.rec.Snapshot().ServerTiming(time.Since(rw.start)))
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

func LoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx, rec := timing.NewContext(r.Context())

		rw := &responseWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
			start:          start,
			rec:            rec,
		}

		reqID := chimw.GetReqID(r.Context())

		log.Printf(
			"[REQUEST] id=%s method=%s path=%s query=%s ip=%s ua=%q",
			reqID,
			r.Method,
			r.URL.Path,
			r.URL.RawQuery,
			ClientIP(r),
			r.UserAgent(),
		)

		next.ServeHTTP(rw, r.WithContext(ctx))

		snap := rec.Snapshot()
		log.Printf(
			"[RESPONSE] id=%s status=%d duration_ms=%d db_queries=%d db_ms=%d tmdb_calls=%d tmdb_ms=%d",
			reqID,
			rw.status,
			time.Since(start).Milliseconds(),
			snap.DBQueries,
			snap.DB.Milliseconds(),
			snap.TMDBCalls,
			snap.TMDB.Milliseconds(),
		)
	})
}
