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
	UsernameExists(ctx context.Context, name string) (bool, error)
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

// UpdateMe applies a profile patch. A username, when provided, must pass
// ValidateUsername. Other fields are passed through as-is — length and format
// constraints on those are a frontend concern at this stage.
//
// Collisions are not pre-checked here: the unique index is the only race-free
// answer, and the repository already translates it into ErrUsernameTaken.
func (s *Service) UpdateMe(ctx context.Context, id uuid.UUID, req UpdateProfileRequest) (*User, error) {
	if req.Username != nil {
		if err := ValidateUsername(*req.Username); err != nil {
			return nil, err
		}
	}
	return s.repo.UpdateProfile(ctx, id, UpdateProfilePatch{
		Username:  req.Username,
		Bio:       req.Bio,
		AvatarURL: req.AvatarURL,
		BannerURL: req.BannerURL,
	})
}

// UsernameAvailability is the answer to "can I have this name?". Reason is
// null when Available is true, and otherwise one of the Reason* constants —
// the client shows a different message for each, so a bare boolean is not
// enough.
type UsernameAvailability struct {
	Username  string  `json:"username"`
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
}

// CheckUsernameAvailable answers for an unauthenticated caller during sign-up.
// It reports format and reservation problems without touching the database,
// and only queries when the name is otherwise usable.
//
// This necessarily leaks "does this name exist", which is unavoidable for the
// feature to work and acceptable because usernames are public display names.
// What must never leak from here is any mapping to an email address.
func (s *Service) CheckUsernameAvailable(ctx context.Context, name string) (*UsernameAvailability, error) {
	if err := ValidateUsername(name); err != nil {
		return unavailable(name, UnavailableReason(err)), nil
	}

	taken, err := s.repo.UsernameExists(ctx, name)
	if err != nil {
		return nil, err
	}
	if taken {
		return unavailable(name, ReasonTaken), nil
	}
	return &UsernameAvailability{Username: name, Available: true}, nil
}

func unavailable(name, reason string) *UsernameAvailability {
	return &UsernameAvailability{Username: name, Available: false, Reason: &reason}
}

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
