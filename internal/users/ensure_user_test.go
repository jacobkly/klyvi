package users_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/users"
)

// ensureCountingRepo counts EnsureUser calls per user.
type ensureCountingRepo struct {
	fakeUserRepo

	mu      sync.Mutex
	ensured map[uuid.UUID]int
}

func (r *ensureCountingRepo) EnsureUser(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensured[id]++
	return nil
}

func (r *ensureCountingRepo) DeleteAccountData(context.Context, uuid.UUID) error { return nil }

func serveAs(t *testing.T, h http.Handler, id uuid.UUID) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
	req = req.WithContext(middleware.WithUserUUID(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

// Every authenticated request used to pay a users-row upsert round trip. The
// row only needs creating once per user.
func TestEnsureUserMiddleware_InsertsOncePerUser(t *testing.T) {
	repo := &ensureCountingRepo{ensured: map[uuid.UUID]int{}}
	svc := users.NewService(repo).WithAccountDeletion(&fakeAuthDeleter{}, "avatars")
	h := svc.EnsureUserMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	alice, bob := uuid.New(), uuid.New()
	serveAs(t, h, alice)
	serveAs(t, h, alice)
	serveAs(t, h, bob)
	serveAs(t, h, alice)

	if repo.ensured[alice] != 1 || repo.ensured[bob] != 1 {
		t.Fatalf("EnsureUser calls: alice=%d bob=%d, want 1 each", repo.ensured[alice], repo.ensured[bob])
	}

	// A deleted account that signs straight back in needs its row again.
	if err := svc.DeleteAccount(context.Background(), alice); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	serveAs(t, h, alice)
	if repo.ensured[alice] != 2 {
		t.Errorf("EnsureUser calls for alice after account deletion = %d, want 2", repo.ensured[alice])
	}
}
