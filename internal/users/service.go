package users

import (
	"context"
	"log"
	"net/http"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
)

// UserRepository is the persistence boundary the users service needs.
type UserRepository interface {
	EnsureUser(ctx context.Context, id uuid.UUID) error
	GetUserByID(ctx context.Context, id uuid.UUID) (*User, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, patch UpdateProfilePatch) (*User, error)
}

type Service struct {
	repo UserRepository
}

func NewService(repo UserRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) EnsureUser(ctx context.Context, id uuid.UUID) error {
	return s.repo.EnsureUser(ctx, id)
}

func (s *Service) GetMe(ctx context.Context, id uuid.UUID) (*User, error) {
	return s.repo.GetUserByID(ctx, id)
}

// UpdateProfileRequest is the validated body for PATCH /v1/users/me.
type UpdateProfileRequest struct {
	Username  *string `json:"username"`
	Bio       *string `json:"bio"`
	AvatarURL *string `json:"avatar_url"`
	BannerURL *string `json:"banner_url"`
}

// UpdateMe applies a profile patch. Light validation: username, when
// provided, must be 3..40 chars. Other fields are passed through as-is —
// length/format constraints are a frontend concern at this stage.
func (s *Service) UpdateMe(ctx context.Context, id uuid.UUID, req UpdateProfileRequest) (*User, error) {
	if req.Username != nil {
		n := len(*req.Username)
		if n < 3 || n > 40 {
			return nil, errInvalidUsername
		}
	}
	return s.repo.UpdateProfile(ctx, id, UpdateProfilePatch{
		Username:  req.Username,
		Bio:       req.Bio,
		AvatarURL: req.AvatarURL,
		BannerURL: req.BannerURL,
	})
}

var errInvalidUsername = &validationErr{msg: "username must be 3..40 characters"}

type validationErr struct{ msg string }

func (e *validationErr) Error() string { return e.msg }

// EnsureUserMiddleware upserts the `users` row on every authenticated request.
// Mount it AFTER the JWT auth middleware on protected routes — it assumes the
// user UUID is already in context. Failures are logged best-effort and do not
// reject the request, on the assumption that catalog state is more useful to
// the user than a 5xx if the upsert briefly fails.
func (s *Service) EnsureUserMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, ok := middleware.UserUUIDFromContext(r.Context()); ok {
				if err := s.repo.EnsureUser(r.Context(), id); err != nil {
					log.Printf("best-effort EnsureUser failed for %s: %v", id, err)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
