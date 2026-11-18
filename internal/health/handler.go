package health

import (
	"context"
	"log"
	"net/http"
	"time"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

func Get(w http.ResponseWriter, r *http.Request) {
	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		map[string]interface{}{
			"status_code": 200,
			"details":     "Server is up and running.",
		},
	)
}

// ReadyTimeout caps the database ping, so a hung database reads as down to
// the uptime monitor instead of holding the check open.
const ReadyTimeout = 2 * time.Second

// Pinger is the database as the readiness check sees it. *sqlx.DB has it.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Ready answers 200 when the database answers a ping within ReadyTimeout and
// 503 when it does not. /health says the process is up. This says it can
// serve a request.
func Ready(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), ReadyTimeout)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			log.Printf("readiness: database ping failed: %v", err)
			response.WriteError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		response.WriteSuccess(
			w,
			http.StatusOK,
			"v1",
			middleware.StatsFromContext(r.Context()),
			map[string]interface{}{
				"status_code": 200,
				"details":     "Server and database are up.",
			},
		)
	}
}
