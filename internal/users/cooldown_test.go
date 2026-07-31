package users_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"klyvi-api/internal/users"
)

func ptr[T any](v T) *T { return &v }

// The cooldown decides whether a save the user already committed to
// succeeds, so its boundaries are worth stating precisely.
func TestUpdateMe_Cooldown(t *testing.T) {
	const cooldown = users.UsernameChangeCooldown

	tests := []struct {
		name string
		// current is the stored row the patch is applied to.
		currentName    string
		changedAt      *time.Time
		patch          users.UpdateProfileRequest
		wantCooldown   bool
		wantRetryAfter time.Duration
	}{
		{
			name:        "never changed, so the first change is free",
			currentName: "user_a1b2c3d4",
			changedAt:   nil,
			patch:       users.UpdateProfileRequest{Username: ptr("jacob")},
		},
		{
			name:        "changed 13 days ago is still inside the cooldown",
			currentName: "jacob",
			changedAt:   ptr(time.Now().Add(-13 * 24 * time.Hour)),
			patch:       users.UpdateProfileRequest{Username: ptr("jacobk")},
			// 13 days elapsed of 14 leaves roughly a day.
			wantCooldown:   true,
			wantRetryAfter: 23 * time.Hour,
		},
		{
			name:        "changed 15 days ago is outside the cooldown",
			currentName: "jacob",
			changedAt:   ptr(time.Now().Add(-15 * 24 * time.Hour)),
			patch:       users.UpdateProfileRequest{Username: ptr("jacobk")},
		},
		{
			name:         "changed just now is refused",
			currentName:  "jacob",
			changedAt:    ptr(time.Now()),
			patch:        users.UpdateProfileRequest{Username: ptr("jacobk")},
			wantCooldown: true,
			// The deadline is the full cooldown away, less the moment spent
			// getting here.
			wantRetryAfter: cooldown - time.Minute,
		},
		{
			name:        "resubmitting the same name is not a change",
			currentName: "jacob",
			changedAt:   ptr(time.Now()),
			patch:       users.UpdateProfileRequest{Username: ptr("jacob")},
		},
		{
			name:        "fixing only capitalisation is free",
			currentName: "jacob",
			changedAt:   ptr(time.Now()),
			patch:       users.UpdateProfileRequest{Username: ptr("Jacob")},
		},
		{
			name:        "a bio-only patch never trips the cooldown",
			currentName: "jacob",
			changedAt:   ptr(time.Now()),
			patch:       users.UpdateProfileRequest{Bio: ptr("watches too much")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeUserRepo{user: &users.User{
				ID:                uuid.New(),
				Username:          tt.currentName,
				UsernameChangedAt: tt.changedAt,
			}}
			svc := users.NewService(repo)

			_, err := svc.UpdateMe(context.Background(), repo.user.ID, tt.patch)

			var cd *users.UsernameCooldownError
			gotCooldown := errors.As(err, &cd)

			if gotCooldown != tt.wantCooldown {
				t.Fatalf("cooldown = %v (err %v), want %v", gotCooldown, err, tt.wantCooldown)
			}
			if !tt.wantCooldown {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if until := time.Until(cd.RetryAt); until < tt.wantRetryAfter {
				t.Errorf("RetryAt is %v away, want at least %v", until, tt.wantRetryAfter)
			}
		})
	}
}

// The deadline is what the client shows instead of failing a save, so it has
// to be readable and land exactly one cooldown after the last change.
func TestUsernameCooldownError_Message(t *testing.T) {
	retryAt := time.Date(2026, time.August, 14, 9, 30, 0, 0, time.UTC)
	err := &users.UsernameCooldownError{RetryAt: retryAt}

	const want = "username can be changed again on 2026-08-14T09:30:00Z"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
