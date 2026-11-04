package users

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// AccountDeleter is the external side of account deletion: the storage
// object and the Supabase auth user. Satisfied by *supabase.Client.
type AccountDeleter interface {
	DeleteObject(ctx context.Context, bucket, path string) error
	DeleteAuthUser(ctx context.Context, userID string) error
}

// WithAccountDeletion wires the Supabase client used to remove the avatar
// object and the auth user during account deletion. Without it, DeleteAccount
// returns ErrStorageNotConfigured — deleting app data but stranding the auth
// user would be worse than refusing.
func (s *Service) WithAccountDeletion(deleter AccountDeleter, bucket string) *Service {
	s.deleter = deleter
	if s.bucket == "" {
		s.bucket = bucket
	}
	return s
}

// DeleteAccount removes everything tied to a user, in an order chosen so a
// mid-way failure is always recoverable:
//
//  1. App data (media_list, interactions, taste_profiles, users row) in one
//     transaction — all or nothing.
//  2. The avatar object — best-effort; a 404 is already success, and a
//     transient failure here should not strand the account half-deleted.
//  3. The Supabase auth user — last, because it is the irreversible step
//     with no local record.
//
// If step 1 fails, nothing is gone and the client can retry. If step 3
// fails, the app data is already gone; the next sign-in re-creates a fresh
// empty row and the client retries the delete. The one order we must avoid
// is deleting the auth user first and then failing to remove the data.
func (s *Service) DeleteAccount(ctx context.Context, id uuid.UUID) error {
	if s.deleter == nil {
		return ErrStorageNotConfigured
	}

	if err := s.repo.DeleteAccountData(ctx, id); err != nil {
		return fmt.Errorf("delete account data: %w", err)
	}
	// The users row is gone. If removing the auth user fails below, the client
	// comes back with the same token to retry, and that request needs the row.
	s.ensured.Remove(id.String())
	if s.feed != nil {
		s.feed.Invalidate(id)
	}

	// Best-effort: the object may never have existed. The supabase client
	// maps 404 to nil, so only a real transport failure surfaces, and even
	// then we prefer to continue — a stray avatar object is a smaller
	// problem than a half-deleted account.
	_ = s.deleter.DeleteObject(ctx, s.bucket, avatarObjectPath(id))

	if err := s.deleter.DeleteAuthUser(ctx, id.String()); err != nil {
		return fmt.Errorf("delete auth user: %w", err)
	}
	// Only now is the account really gone. Its token stays valid for up to an
	// hour, so refuse it rather than let it recreate the row.
	s.deleted.Set(id.String(), struct{}{})
	return nil
}
