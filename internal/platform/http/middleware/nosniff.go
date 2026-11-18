package middleware

import "net/http"

// Nosniff tells browsers to take each response's Content-Type as given and
// never guess, so a JSON body can't be read as a script or a page.
func Nosniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
