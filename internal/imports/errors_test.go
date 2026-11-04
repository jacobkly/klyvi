package imports_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/imports"
)

// dbDownRepo fails Create the way an outage would.
type dbDownRepo struct{ imports.Repo }

func (dbDownRepo) Create(context.Context, uuid.UUID, string) (*imports.Import, error) {
	return nil, errors.New("pq: password authentication failed for user klyvi")
}

func createImport(t *testing.T, source, file string) (int, string) {
	t.Helper()
	api := imports.NewAPI(imports.NewService(dbDownRepo{}, nil, nil, nil, nil))
	rec := httptest.NewRecorder()
	api.Create(rec, importRequest(t, source, file))

	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return rec.Code, body.Error
}

// A database failure used to come back as a 400 carrying the driver's text.
func TestCreate_DatabaseFailureIsAGeneric500(t *testing.T) {
	status, msg := createImport(t, "letterboxd", "Date,Name,Year,Rating\n2024-01-02,Parasite,2019,4.5\n")

	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if msg != "internal server error" {
		t.Errorf("error = %q, want the generic message", msg)
	}
}

// The user can act on these, so they keep their text.
func TestCreate_BadFilesKeepTheirMessage(t *testing.T) {
	tests := []struct {
		name, source, file, want string
	}{
		{"an unknown source", "netflix", "x", "source must be"},
		{"a file that is not an export", "letterboxd", "this is not a csv header\n", "could not parse import file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := createImport(t, tt.source, tt.file)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", status)
			}
			if !strings.Contains(msg, tt.want) {
				t.Errorf("error = %q, want it to contain %q", msg, tt.want)
			}
		})
	}
}
