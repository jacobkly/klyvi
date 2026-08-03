package users_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/users"
)

// fakeUserRepo lets the status-code contract be tested without a database.
// The web client branches on these codes, so they are the part worth pinning.
type fakeUserRepo struct {
	user       *users.User
	updateErr  error
	existsResp bool
	existsErr  error

	// lastPatch records what UpdateProfile received, so tests can assert
	// tri-state semantics (absent vs clear vs set) actually reached the repo.
	lastPatch users.UpdateProfilePatch
}

func (f *fakeUserRepo) EnsureUser(context.Context, uuid.UUID) error { return nil }

func (f *fakeUserRepo) GetUserByID(context.Context, uuid.UUID) (*users.User, error) {
	return f.user, nil
}

func (f *fakeUserRepo) UpdateProfile(_ context.Context, _ uuid.UUID, patch users.UpdateProfilePatch) (*users.User, error) {
	f.lastPatch = patch
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	out := *f.user
	if patch.Username != nil {
		out.Username = *patch.Username
	}
	return &out, nil
}

func (f *fakeUserRepo) UsernameExists(context.Context, string) (bool, error) {
	return f.existsResp, f.existsErr
}

func patchUsername(t *testing.T, repo *fakeUserRepo, body string) *httptest.ResponseRecorder {
	t.Helper()

	api := users.NewAPI(users.NewService(repo))
	req := httptest.NewRequest(http.MethodPatch, "/v1/users/me", strings.NewReader(body))
	req = req.WithContext(middleware.WithUserUUID(req.Context(), uuid.New()))

	rec := httptest.NewRecorder()
	api.UpdateMe(rec, req)
	return rec
}

func TestUpdateMe_StatusCodes(t *testing.T) {
	base := &users.User{ID: uuid.New(), Username: "user_a1b2c3d4"}

	tests := []struct {
		name       string
		body       string
		updateErr  error
		wantStatus int
	}{
		{
			name:       "valid username succeeds",
			body:       `{"username":"jacob"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "too short is a format problem",
			body:       `{"username":"ab"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "disallowed characters are a format problem",
			body:       `{"username":"jacob k"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "reserved name conflicts",
			body:       `{"username":"admin"}`,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "taken name conflicts",
			body:       `{"username":"jacob"}`,
			updateErr:  users.ErrUsernameTaken,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "cooldown is rate limited",
			body:       `{"username":"jacob"}`,
			updateErr:  &users.UsernameCooldownError{RetryAt: time.Now().Add(48 * time.Hour)},
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "bio-only patch skips username validation",
			body:       `{"bio":"watches too much"}`,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := patchUsername(t, &fakeUserRepo{user: base, updateErr: tt.updateErr}, tt.body)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// A duplicate must never reach the browser as a driver error. The client used
// to regex-match that string; this asserts it is gone for good.
func TestUpdateMe_DoesNotLeakDriverError(t *testing.T) {
	repo := &fakeUserRepo{
		user:      &users.User{ID: uuid.New(), Username: "user_a1b2c3d4"},
		updateErr: users.ErrUsernameTaken,
	}

	rec := patchUsername(t, repo, `{"username":"jacob"}`)

	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, leak := range []string{"SQLSTATE", "23505", "duplicate key", "users_username_lower_key"} {
		if strings.Contains(body.Error, leak) {
			t.Errorf("response leaks %q: %s", leak, body.Error)
		}
	}
	if body.Error != users.ErrUsernameTaken.Error() {
		t.Errorf("error = %q, want %q", body.Error, users.ErrUsernameTaken.Error())
	}
}

func TestCheckUsernameAvailable(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		exists    bool
		available bool
		reason    string
	}{
		{"free name", "jacob", false, true, ""},
		{"taken name", "jacob", true, false, users.ReasonTaken},
		{"reserved name", "admin", false, false, users.ReasonReserved},
		{"invalid format", "jacob k", false, false, users.ReasonInvalid},
		{"too short", "ab", false, false, users.ReasonInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := users.NewService(&fakeUserRepo{existsResp: tt.exists})
			got, err := svc.CheckUsernameAvailable(context.Background(), tt.input)
			if err != nil {
				t.Fatalf("CheckUsernameAvailable: %v", err)
			}
			if got.Available != tt.available {
				t.Errorf("available = %v, want %v", got.Available, tt.available)
			}
			if reason := derefReason(got.Reason); reason != tt.reason {
				t.Errorf("reason = %q, want %q", reason, tt.reason)
			}
			// An available name reports a null reason, not an empty string.
			if tt.available && got.Reason != nil {
				t.Errorf("reason = %q on an available name, want null", *got.Reason)
			}
			if got.Username != tt.input {
				t.Errorf("username = %q, want %q", got.Username, tt.input)
			}
		})
	}
}

// A reserved or malformed name must be answered without a database round
// trip, so the endpoint cannot be used to hammer the users table with junk.
func TestCheckUsernameAvailable_SkipsQueryForUnusableNames(t *testing.T) {
	repo := &fakeUserRepo{existsErr: errUnexpectedQuery}
	svc := users.NewService(repo)

	for _, name := range []string{"admin", "ab", "jacob k"} {
		if _, err := svc.CheckUsernameAvailable(context.Background(), name); err != nil {
			t.Errorf("CheckUsernameAvailable(%q) queried the database: %v", name, err)
		}
	}
}

func derefReason(r *string) string {
	if r == nil {
		return ""
	}
	return *r
}

var errUnexpectedQuery = errUnexpected{}

type errUnexpected struct{}

func (errUnexpected) Error() string { return "database should not have been queried" }
