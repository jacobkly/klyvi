package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// AdminOnly lets the listed users through and answers everyone else, signed
// in or not, with the same plain 404 an unknown route gets, so the admin
// routes don't show they exist. It reads the user an auth middleware put in
// the context, so mount it after one (the optional one, so a signed-out
// visitor gets the 404 too instead of a 401). What passes is never cached.
func AdminOnly(admins []uuid.UUID) func(http.Handler) http.Handler {
	allowed := make(map[uuid.UUID]bool, len(admins))
	for _, id := range admins {
		allowed[id] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := UserUUIDFromContext(r.Context())
			if !ok || !allowed[id] {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	}
}

// ParseUserIDs reads a comma-separated list of user ids, as ADMIN_USER_IDS
// holds. Blank entries are skipped. A malformed id is an error, so a typo
// fails startup instead of quietly locking the admin out.
func ParseUserIDs(s string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, fmt.Errorf("parse user id %q: %w", part, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
