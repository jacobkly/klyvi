package movies

import (
	"errors"
	"log"
	"net/http"

	"klyvi-api/internal/platform/http/response"
	"klyvi-api/internal/platform/tmdb"
)

// Sentinel errors the service returns for client-caused failures. Everything
// else (TMDB, database) is an internal fault and maps to 500.
var (
	ErrInvalidIDType   = errors.New("invalid id_type: must be tmdb or media")
	ErrInvalidListType = errors.New("invalid list type")
	ErrMovieNotFound   = errors.New("movie not found")
)

// statusForError maps a service error to an HTTP status and a client-safe
// message. Validation and not-found errors surface their own message; anything
// else is treated as an internal fault with a generic message, so no upstream
// or database detail leaks to the client.
func statusForError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrInvalidIDType), errors.Is(err, ErrInvalidListType):
		return http.StatusBadRequest, err.Error()
	// TMDB answering 404 means the id names no title, which is the
	// visitor's mistake, not ours. Any other TMDB status stays a 500.
	case errors.Is(err, ErrMovieNotFound), tmdb.IsStatus(err, http.StatusNotFound):
		return http.StatusNotFound, "movie not found"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// respondError writes the mapped error, logging internal faults once.
func respondError(w http.ResponseWriter, err error) {
	status, msg := statusForError(err)
	if status == http.StatusInternalServerError {
		log.Printf("movies: %v", err)
	}
	response.WriteError(w, status, msg)
}
