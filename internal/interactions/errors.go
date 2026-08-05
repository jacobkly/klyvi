package interactions

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"klyvi-api/internal/platform/http/response"
)

// ValidationError marks a client input failure and maps to 400. Anything the
// service returns that is not a ValidationError is an internal fault (500).
type ValidationError struct{ msg string }

func (e ValidationError) Error() string { return e.msg }

// invalid builds a ValidationError with a formatted message.
func invalid(format string, a ...any) error {
	return ValidationError{msg: fmt.Sprintf(format, a...)}
}

// statusForError maps a service error to an HTTP status and a client-safe
// message. Validation failures surface their message; anything else is an
// internal fault with a generic message so no database detail leaks.
func statusForError(err error) (int, string) {
	var ve ValidationError
	if errors.As(err, &ve) {
		return http.StatusBadRequest, ve.Error()
	}
	return http.StatusInternalServerError, "internal server error"
}

// respondError writes the mapped error, logging internal faults once.
func respondError(w http.ResponseWriter, err error) {
	status, msg := statusForError(err)
	if status == http.StatusInternalServerError {
		log.Printf("interactions: %v", err)
	}
	response.WriteError(w, status, msg)
}
