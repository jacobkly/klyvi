package users_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/users"
)

// Settings are an opaque client-owned blob: the server enforces only that it
// is a JSON object and fits the size cap. Everything else is the client's
// business, which is what lets the UI settings set churn without migrations.
func TestUpdateMe_Settings(t *testing.T) {
	raw := func(s string) *json.RawMessage {
		m := json.RawMessage(s)
		return &m
	}

	tests := []struct {
		name     string
		settings *json.RawMessage
		wantErr  error
	}{
		{"replace with a real blob", raw(`{"themeMode":"dark","textSize":"m"}`), nil},
		{"empty object is fine", raw(`{}`), nil},
		{"nested objects are fine", raw(`{"notifications":{"weekly":true}}`), nil},
		{"absent means unchanged", nil, nil},

		// Shape: must be a JSON object, not any valid JSON.
		{"array is rejected", raw(`[1,2,3]`), users.ErrSettingsInvalid},
		{"string is rejected", raw(`"dark"`), users.ErrSettingsInvalid},
		{"number is rejected", raw(`42`), users.ErrSettingsInvalid},
		{"null is rejected", raw(`null`), users.ErrSettingsInvalid},
		{"malformed json is rejected", raw(`{"broken`), users.ErrSettingsInvalid},

		// Size: 8 KB cap.
		{
			"oversized blob is rejected",
			raw(`{"pad":"` + strings.Repeat("x", 8*1024) + `"}`),
			users.ErrSettingsTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeUserRepo{user: &users.User{ID: uuid.New(), Username: "jacob"}}
			svc := users.NewService(repo)

			_, err := svc.UpdateMe(context.Background(), repo.user.ID, users.UpdateProfileRequest{
				Settings: tt.settings,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("UpdateMe settings error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// The two settings failures map to 400 like other validation problems.
func TestUpdateMe_SettingsStatusCodes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"valid settings", `{"settings":{"themeMode":"dark"}}`, http.StatusOK},
		{"non-object settings", `{"settings":[1]}`, http.StatusBadRequest},
		{"oversized settings", `{"settings":{"pad":"` + strings.Repeat("x", 8*1024) + `"}}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeUserRepo{user: &users.User{ID: uuid.New(), Username: "jacob"}}
			rec := patchUsername(t, repo, tt.body)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}
