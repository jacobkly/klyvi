package middleware

import (
	"context"

	"github.com/google/uuid"
)

// userUUIDKey is the private context key used to stash the authenticated
// user's UUID. Defined as a typed unexported value to prevent collisions with
// keys defined in other packages.
type userUUIDCtxKey struct{}

// WithUserUUID returns a child context carrying the authenticated user's UUID.
// The auth middleware is the only place that should call this in production.
func WithUserUUID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userUUIDCtxKey{}, id)
}

// UserUUIDFromContext extracts the authenticated user's UUID. The bool return
// is false when the context carries no user (i.e. the request hit a public
// route or the middleware was bypassed).
func UserUUIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userUUIDCtxKey{}).(uuid.UUID)
	return id, ok
}
