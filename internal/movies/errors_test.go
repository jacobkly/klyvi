package movies

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestStatusForError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"invalid id_type", ErrInvalidIDType, http.StatusBadRequest},
		{"invalid list type", ErrInvalidListType, http.StatusBadRequest},
		{"not found", ErrMovieNotFound, http.StatusNotFound},
		{"wrapped not found", fmt.Errorf("lookup id 5: %w", ErrMovieNotFound), http.StatusNotFound},
		{"infrastructure error", errors.New("pq: connection refused"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := statusForError(tt.err)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			// A 500 must never echo the raw error text back to the client.
			if status == http.StatusInternalServerError && msg == tt.err.Error() {
				t.Errorf("internal error leaked to client: %q", msg)
			}
		})
	}
}
