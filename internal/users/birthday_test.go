package users_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"klyvi-api/internal/users"
)

// Birthday is the first tri-state field on the profile PATCH: absent means
// unchanged, JSON null clears, a value sets. The existing COALESCE fields
// cannot express "clear", which is why this one gets its own semantics.
func TestUpdateMe_Birthday(t *testing.T) {
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	tests := []struct {
		name    string
		body    string // raw JSON body so absent-vs-null is exact
		wantErr error
		// wantBirthday inspects the patch the repo received:
		// "unchanged" | "cleared" | a YYYY-MM-DD value.
		wantBirthday string
	}{
		{"set a real date", `{"birthday":"1999-04-12"}`, nil, "1999-04-12"},
		{"absent leaves it unchanged", `{"bio":"hi"}`, nil, "unchanged"},
		{"null clears it", `{"birthday":null}`, nil, "cleared"},
		{"oldest allowed year", `{"birthday":"1900-01-01"}`, nil, "1900-01-01"},

		{"future date rejected", `{"birthday":"` + tomorrow + `"}`, users.ErrBirthdayInvalid, ""},
		{"before 1900 rejected", `{"birthday":"1899-12-31"}`, users.ErrBirthdayInvalid, ""},
		{"malformed date rejected", `{"birthday":"12/04/1999"}`, users.ErrBirthdayInvalid, ""},
		{"non-string rejected", `{"birthday":19990412}`, users.ErrBirthdayInvalid, ""},
		{"impossible date rejected", `{"birthday":"1999-02-30"}`, users.ErrBirthdayInvalid, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeUserRepo{user: &users.User{ID: uuid.New(), Username: "jacob"}}
			svc := users.NewService(repo)

			req, err := users.DecodeUpdateProfileRequest([]byte(tt.body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			_, err = svc.UpdateMe(context.Background(), repo.user.ID, req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UpdateMe birthday error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}

			got := repo.lastPatch
			switch tt.wantBirthday {
			case "unchanged":
				if got.Birthday.Set {
					t.Errorf("absent birthday reached the repo as a change: %+v", got.Birthday)
				}
			case "cleared":
				if !got.Birthday.Set || got.Birthday.Value != nil {
					t.Errorf("null birthday should clear, repo got %+v", got.Birthday)
				}
			default:
				if !got.Birthday.Set || got.Birthday.Value == nil {
					t.Fatalf("birthday value did not reach the repo: %+v", got.Birthday)
				}
				if v := got.Birthday.Value.Format("2006-01-02"); v != tt.wantBirthday {
					t.Errorf("repo got birthday %s, want %s", v, tt.wantBirthday)
				}
			}
		})
	}
}
