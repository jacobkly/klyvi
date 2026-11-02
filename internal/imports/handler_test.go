package imports_test

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/imports"
	"klyvi-api/internal/platform/http/middleware"
)

// busyRepo answers Create the way the real repository does when the user
// already has an import pending or running.
type busyRepo struct{ imports.Repo }

func (busyRepo) Create(context.Context, uuid.UUID, string) (*imports.Import, error) {
	return nil, imports.ErrImportActive
}

func importRequest(t *testing.T, file string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("source", "letterboxd"); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", "ratings.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(file)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/imports", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req.WithContext(middleware.WithUserUUID(req.Context(), uuid.New()))
}

// The web client branches on these codes to explain a refused upload, so they
// are the part worth pinning.
func TestCreate_RefusalStatusCodes(t *testing.T) {
	var overCap strings.Builder
	overCap.WriteString("Date,Name,Year,Rating\n")
	for i := range 10_001 {
		fmt.Fprintf(&overCap, "2024-01-02,Film %d,2001,4\n", i)
	}

	tests := []struct {
		name string
		repo imports.Repo
		file string
		want int
	}{
		{
			name: "a second import while one is active",
			repo: busyRepo{},
			file: "Date,Name,Year,Rating\n2024-01-02,Parasite,2019,4.5\n",
			want: http.StatusConflict,
		},
		{
			name: "a file over the row cap",
			repo: busyRepo{},
			file: overCap.String(),
			want: http.StatusRequestEntityTooLarge,
		},
		{
			name: "a file that is not a csv export",
			repo: busyRepo{},
			file: "this is not a csv header\n",
			want: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := imports.NewAPI(imports.NewService(tt.repo, nil, nil, nil, nil))
			rec := httptest.NewRecorder()

			api.Create(rec, importRequest(t, tt.file))

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}
