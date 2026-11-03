package imports_test

import (
	"archive/zip"
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

func importRequest(t *testing.T, source, file string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("source", source); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", "upload")
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

// zipBomb is a small ZIP whose one JSON file unpacks to more than 256 MB,
// almost all of it whitespace.
func zipBomb(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("watched-history.json")
	if err != nil {
		t.Fatal(err)
	}
	spaces := bytes.Repeat([]byte(" "), 1<<20)
	if _, err := f.Write([]byte("[")); err != nil {
		t.Fatal(err)
	}
	for range 257 {
		if _, err := f.Write(spaces); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Write([]byte("]")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
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
		name   string
		repo   imports.Repo
		source string
		file   string
		want   int
	}{
		{
			name:   "a second import while one is active",
			repo:   busyRepo{},
			source: "letterboxd",
			file:   "Date,Name,Year,Rating\n2024-01-02,Parasite,2019,4.5\n",
			want:   http.StatusConflict,
		},
		{
			name:   "a file over the row cap",
			repo:   busyRepo{},
			source: "letterboxd",
			file:   overCap.String(),
			want:   http.StatusRequestEntityTooLarge,
		},
		{
			name:   "a file that is not a csv export",
			repo:   busyRepo{},
			source: "letterboxd",
			file:   "this is not a csv header\n",
			want:   http.StatusBadRequest,
		},
		{
			name:   "a trakt export that unpacks too large",
			repo:   busyRepo{},
			source: "trakt",
			file:   zipBomb(t),
			want:   http.StatusRequestEntityTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := imports.NewAPI(imports.NewService(tt.repo, nil, nil, nil, nil))
			rec := httptest.NewRecorder()

			api.Create(rec, importRequest(t, tt.source, tt.file))

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}
