package tv

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
		{"invalid type", ErrInvalidIDType, http.StatusBadRequest},
		{"invalid list type", ErrInvalidListType, http.StatusBadRequest},
		{"not found", ErrTVNotFound, http.StatusNotFound},
		{"wrapped not found", fmt.Errorf("resolve media 9: %w", ErrTVNotFound), http.StatusNotFound},
		{"infrastructure error", errors.New("pq: connection refused"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := statusForError(tt.err)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if status == http.StatusInternalServerError && msg == tt.err.Error() {
				t.Errorf("internal error leaked to client: %q", msg)
			}
		})
	}
}
