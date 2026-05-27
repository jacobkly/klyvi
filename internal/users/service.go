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
