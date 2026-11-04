package users_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/tracking"
	"klyvi-api/internal/users"
)

const driverError = `pq: invalid byte sequence for encoding "UTF8": 0x00`

// failingGetRepo fails the profile read the way a database outage would.
type failingGetRepo struct{ fakeUserRepo }

func (*failingGetRepo) GetUserByID(context.Context, uuid.UUID) (*users.User, error) {
	return nil, errors.New(driverError)
}

// failingStorage fails the way an upstream storage call would.
type failingStorage struct{}

func (failingStorage) UploadPublicObject(context.Context, string, string, string, []byte) (string, error) {
	return "", errors.New(driverError)
}

func (failingStorage) DeleteObject(context.Context, string, string) error {
	return errors.New(driverError)
}

func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

func authedRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(middleware.WithUserUUID(req.Context(), uuid.New()))
}

// A server fault reaches the client as a generic message and reaches the log
// in full, so the detail is hidden and not lost.
func TestServerFaultsAreLoggedAndHidden(t *testing.T) {
	updateFails := &fakeUserRepo{user: &users.User{}, updateErr: errors.New(driverError)}

	tests := []struct {
		name string
		call func(w http.ResponseWriter)
	}{
		{"read profile", func(w http.ResponseWriter) {
			users.NewAPI(users.NewService(&failingGetRepo{})).
				GetMe(w, authedRequest(http.MethodGet, "/v1/users/me", ""))
		}},
		{"a bio postgres refuses", func(w http.ResponseWriter) {
			users.NewAPI(users.NewService(updateFails)).
				UpdateMe(w, authedRequest(http.MethodPatch, "/v1/users/me", `{"bio":"a\u0000b"}`))
		}},
		{"remove avatar", func(w http.ResponseWriter) {
			users.NewAPI(users.NewService(&fakeUserRepo{user: &users.User{}}).WithAvatarStorage(failingStorage{}, "avatars")).
				DeleteAvatar(w, authedRequest(http.MethodDelete, "/v1/users/me/avatar", ""))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(prev) })

			rec := httptest.NewRecorder()
			tt.call(rec)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			if got := errorMessage(t, rec); got != "internal server error" {
				t.Errorf("error = %q, want the generic message", got)
			}
			if !strings.Contains(logged.String(), "invalid byte sequence") {
				t.Errorf("the log lost the cause: %q", logged.String())
			}
		})
	}
}

// This one is fixed text naming a deployment problem, and the docs promise it.
func TestStorageNotConfiguredKeepsItsMessage(t *testing.T) {
	api := users.NewAPI(users.NewService(&fakeUserRepo{user: &users.User{}}))

	rec := httptest.NewRecorder()
	api.DeleteAvatar(rec, authedRequest(http.MethodDelete, "/v1/users/me/avatar", ""))

	if got := errorMessage(t, rec); got != users.ErrStorageNotConfigured.Error() {
		t.Errorf("error = %q, want %q", got, users.ErrStorageNotConfigured.Error())
	}
}

// The decoder's text names Go types, and the engine behind it can change.
func TestUpdateMe_MalformedBodyHasAPlainMessage(t *testing.T) {
	api := users.NewAPI(users.NewService(&fakeUserRepo{user: &users.User{}}))

	rec := httptest.NewRecorder()
	api.UpdateMe(rec, authedRequest(http.MethodPatch, "/v1/users/me", `{"bio":5}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := errorMessage(t, rec); got != "invalid json" {
		t.Errorf("error = %q, want %q", got, "invalid json")
	}
}

// The profile page renders these URLs for anyone viewing it, so a client must
// not be able to point them at another host. Avatars go through the upload
// endpoint, and nothing in the client sets a banner.
func TestUpdateMe_IgnoresImageURLs(t *testing.T) {
	repo := &fakeUserRepo{user: &users.User{}}
	api := users.NewAPI(users.NewService(repo))

	rec := httptest.NewRecorder()
	api.UpdateMe(rec, authedRequest(http.MethodPatch, "/v1/users/me",
		`{"bio":"hi","avatar_url":"https://evil.example/a.png","banner_url":"https://evil.example/b.png"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if repo.lastPatch.AvatarURL != nil {
		t.Errorf("avatar_url reached the repository: %q", *repo.lastPatch.AvatarURL)
	}
	if repo.lastPatch.Bio == nil || *repo.lastPatch.Bio != "hi" {
		t.Errorf("bio = %v, want the rest of the patch to still apply", repo.lastPatch.Bio)
	}
}

// A spreadsheet runs a cell that starts with one of these characters as a
// formula, and these titles come from TMDB, not from the user.
func TestExportCSV_DefusesFormulas(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{`=HYPERLINK("https://evil.example")`, `'=HYPERLINK("https://evil.example")`},
		{"+1", "'+1"},
		{"-1", "'-1"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"\tleading tab", "'\tleading tab"},
		{"\rleading return", "'\rleading return"},
		{"Fight Club", "Fight Club"},
		{"9-1-1", "9-1-1"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			title := tt.title
			src := &fakeExportSource{entries: []tracking.Entry{{MediaID: 1, MediaType: "movie", Title: &title}}}

			data, err := exportSvc(src).ExportTrackingCSV(context.Background(), uuid.New())
			if err != nil {
				t.Fatalf("ExportTrackingCSV: %v", err)
			}
			rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
			if err != nil {
				t.Fatalf("parse csv: %v", err)
			}
			if got := rows[1][2]; got != tt.want {
				t.Errorf("title cell = %q, want %q", got, tt.want)
			}
		})
	}
}

func requestAs(h http.Handler, id uuid.UUID) int {
	req := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
	req = req.WithContext(middleware.WithUserUUID(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// A token stays valid for up to an hour after the account is gone. It must not
// bring the row back or reach any handler.
func TestEnsureUserMiddleware_RefusesADeletedAccount(t *testing.T) {
	repo := &ensureCountingRepo{ensured: map[uuid.UUID]int{}}
	svc := users.NewService(repo).WithAccountDeletion(&fakeAuthDeleter{}, "avatars")
	h := svc.EnsureUserMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	alice, bob := uuid.New(), uuid.New()

	if err := svc.DeleteAccount(context.Background(), alice); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}

	if got := requestAs(h, alice); got != http.StatusUnauthorized {
		t.Errorf("deleted account status = %d, want 401", got)
	}
	if repo.ensured[alice] != 0 {
		t.Errorf("the deleted account's row was recreated %d times", repo.ensured[alice])
	}
	if got := requestAs(h, bob); got != http.StatusNoContent {
		t.Errorf("another user's status = %d, want 204", got)
	}
}

// When the auth user could not be removed the account still exists, and the
// client has to be able to come back and retry the delete.
func TestEnsureUserMiddleware_AllowsARetryAfterAFailedDelete(t *testing.T) {
	repo := &ensureCountingRepo{ensured: map[uuid.UUID]int{}}
	svc := users.NewService(repo).WithAccountDeletion(
		&fakeAuthDeleter{deleteAuthErr: errors.New("auth admin down")}, "avatars")
	h := svc.EnsureUserMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	alice := uuid.New()

	if err := svc.DeleteAccount(context.Background(), alice); err == nil {
		t.Fatal("expected the auth failure to surface")
	}

	if got := requestAs(h, alice); got != http.StatusNoContent {
		t.Errorf("status = %d, want 204 so the delete can be retried", got)
	}
	if repo.ensured[alice] != 1 {
		t.Errorf("EnsureUser calls = %d, want 1, since the row is gone and the account is not", repo.ensured[alice])
	}
}
