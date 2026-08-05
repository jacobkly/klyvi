package interactions

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestStatusForError(t *testing.T) {
	if status, _ := statusForError(invalid("invalid kind %q", "x")); status != http.StatusBadRequest {
		t.Errorf("validation error status = %d, want 400", status)
	}

	// errors.As must still see a wrapped ValidationError.
	if status, _ := statusForError(fmt.Errorf("record: %w", invalid("bad"))); status != http.StatusBadRequest {
		t.Errorf("wrapped validation status = %d, want 400", status)
	}

	status, msg := statusForError(errors.New("pq: connection refused"))
	if status != http.StatusInternalServerError {
		t.Errorf("infrastructure error status = %d, want 500", status)
	}
	if msg == "pq: connection refused" {
		t.Error("internal error leaked to client")
	}
}
