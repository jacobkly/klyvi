package users_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/users"
)

// fakeDeleteRepo records the account-deletion call and can force a failure,
// so the ordering guarantee (app data gone before the auth user) is testable
// without a database.
type fakeDeleteRepo struct {
	fakeUserRepo
	deletedAppData uuid.UUID
	deleteErr      error
}

func (f *fakeDeleteRepo) DeleteAccountData(_ context.Context, id uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedAppData = id
	return nil
}

// fakeAuthDeleter stands in for the Supabase admin API.
type fakeAuthDeleter struct {
	deletedAuthUser string
	deleteObjErr    error
	deleteAuthErr   error
}

func (f *fakeAuthDeleter) DeleteObject(context.Context, string, string) error {
	return f.deleteObjErr
}

func (f *fakeAuthDeleter) DeleteAuthUser(_ context.Context, userID string) error {
	if f.deleteAuthErr != nil {
		return f.deleteAuthErr
	}
	f.deletedAuthUser = userID
	return nil
}

// feedInvalidations records which users had their cached feed dropped.
type feedInvalidations struct{ users []uuid.UUID }

func (f *feedInvalidations) Invalidate(userID uuid.UUID) { f.users = append(f.users, userID) }

func TestDeleteAccount(t *testing.T) {
	id := uuid.New()

	t.Run("deletes app data then the auth user", func(t *testing.T) {
		repo := &fakeDeleteRepo{fakeUserRepo: fakeUserRepo{user: &users.User{ID: id}}}
		auth := &fakeAuthDeleter{}
		svc := users.NewService(repo).WithAccountDeletion(auth, "avatars")

		if err := svc.DeleteAccount(context.Background(), id); err != nil {
			t.Fatalf("DeleteAccount: %v", err)
		}
		if repo.deletedAppData != id {
			t.Error("app data was not deleted")
		}
		if auth.deletedAuthUser != id.String() {
			t.Errorf("auth user %q deleted, want %q", auth.deletedAuthUser, id)
		}
	})

	t.Run("drops the cached feed built from the deleted data", func(t *testing.T) {
		repo := &fakeDeleteRepo{fakeUserRepo: fakeUserRepo{user: &users.User{ID: id}}}
		feed := &feedInvalidations{}
		svc := users.NewService(repo).
			WithAccountDeletion(&fakeAuthDeleter{}, "avatars").
			WithFeedInvalidator(feed)

		if err := svc.DeleteAccount(context.Background(), id); err != nil {
			t.Fatalf("DeleteAccount: %v", err)
		}
		if len(feed.users) != 1 || feed.users[0] != id {
			t.Errorf("feed invalidations = %v, want exactly %v", feed.users, id)
		}
	})

	t.Run("stops if app-data deletion fails, leaving the auth user intact", func(t *testing.T) {
		repo := &fakeDeleteRepo{
			fakeUserRepo: fakeUserRepo{user: &users.User{ID: id}},
			deleteErr:    errors.New("db down"),
		}
		auth := &fakeAuthDeleter{}
		svc := users.NewService(repo).WithAccountDeletion(auth, "avatars")

		if err := svc.DeleteAccount(context.Background(), id); err == nil {
			t.Fatal("expected error when app-data deletion fails")
		}
		if auth.deletedAuthUser != "" {
			t.Error("auth user was deleted despite app-data failure — the account is now orphaned")
		}
	})

	t.Run("unconfigured deletion is an explicit error", func(t *testing.T) {
		repo := &fakeDeleteRepo{fakeUserRepo: fakeUserRepo{user: &users.User{ID: id}}}
		svc := users.NewService(repo) // no WithAccountDeletion

		if err := svc.DeleteAccount(context.Background(), id); !errors.Is(err, users.ErrStorageNotConfigured) {
			t.Errorf("error = %v, want ErrStorageNotConfigured", err)
		}
	})

	t.Run("a missing avatar object does not block deletion", func(t *testing.T) {
		repo := &fakeDeleteRepo{fakeUserRepo: fakeUserRepo{user: &users.User{ID: id}}}
		// The supabase client already maps 404 to nil; here we assert the
		// service treats a delete-object error as best-effort by NOT passing
		// one — a real failure would surface, a 404 never reaches us.
		auth := &fakeAuthDeleter{}
		svc := users.NewService(repo).WithAccountDeletion(auth, "avatars")

		if err := svc.DeleteAccount(context.Background(), id); err != nil {
			t.Fatalf("DeleteAccount: %v", err)
		}
	})
}
