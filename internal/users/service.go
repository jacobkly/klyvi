package users

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/cache"
	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

// MaxSettingsBytes caps the opaque settings blob. Large enough for every UI
// preference the client has sketched, small enough that the column cannot be
// abused as free storage.
const MaxSettingsBytes = 8 * 1024

var (
	ErrSettingsInvalid  = errors.New("settings must be a JSON object")
	ErrSettingsTooLarge = errors.New("settings too large")
)

// validateSettings enforces the only two rules the server owns: the blob
// parses as a JSON object, and it fits the cap. Content is the client's.
func validateSettings(raw json.RawMessage) error {
	if len(raw) > MaxSettingsBytes {
		return ErrSettingsTooLarge
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ErrSettingsInvalid
	}
	// json.Unmarshal accepts the literal null into a map without error;
	// reject it explicitly so "settings": null cannot wipe the blob to a
	// non-object value.
	if probe == nil {
		return ErrSettingsInvalid
	}
	return nil
}

// UserRepository is the persistence boundary the users service needs.
type UserRepository interface {
	EnsureUser(ctx context.Context, id uuid.UUID) error
	GetUserByID(ctx context.Context, id uuid.UUID) (*User, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, patch UpdateProfilePatch) (*User, error)
	UsernameExists(ctx context.Context, name string) (bool, error)
	ClearAvatarURL(ctx context.Context, id uuid.UUID) (*User, error)
	StatsRowsByUser(ctx context.Context, id uuid.UUID) ([]StatsRow, error)
	StatsEventsByUser(ctx context.Context, id uuid.UUID) ([]StatsEvent, error)
	DeleteAccountData(ctx context.Context, id uuid.UUID) error
}

type Service struct {
	repo UserRepository

	// storage is nil unless avatar brokering is configured; the avatar
	// endpoints check and degrade with ErrStorageNotConfigured.
	storage AvatarStorage
	// deleter is nil unless account deletion is configured (same Supabase
	// client). DeleteAccount checks and degrades the same way.
	deleter AccountDeleter
	bucket  string

	// tracking, interactions and notices feed the export endpoints; wired via
	// WithExportSources. Always present in the assembled server.
	tracking     TrackingReader
	interactions InteractionReader
	notices      NoticeReader

	// ensured remembers users whose row already exists, so the middleware
	// skips its upsert round trip. The TTL re-checks now and then, in case a
	// row was removed behind the server's back.
	ensured *cache.TTL[struct{}]

	// deleted remembers accounts removed in the last deletedUsersTTL. Their
	// tokens still verify, and without this one would recreate the users row.
	// A restart forgets the list, which at worst leaves one empty row behind.
	deleted *cache.TTL[struct{}]

	// feed is nil unless the recommender's feed cache is wired; account
	// deletion drops the user's cached feed through it.
	feed FeedInvalidator

	// now is the service's clock. time.Now in production; tests pin it.
	now func() time.Time
}

// FeedInvalidator drops a user's cached recommendations. The reco feed cache
// satisfies it.
type FeedInvalidator interface {
	Invalidate(userID uuid.UUID)
}

// WithFeedInvalidator makes account deletion drop the user's cached feed,
// which was built from the data being deleted.
func (s *Service) WithFeedInvalidator(f FeedInvalidator) *Service {
	s.feed = f
	return s
}

const (
	ensuredUsersMax = 10_000
	ensuredUsersTTL = time.Hour

	deletedUsersMax = 10_000
	// deletedUsersTTL outlives any token issued before the deletion, since
	// access tokens last an hour.
	deletedUsersTTL = 2 * time.Hour
)

func NewService(repo UserRepository) *Service {
	return &Service{
		repo:    repo,
		ensured: cache.NewTTL[struct{}](ensuredUsersMax, ensuredUsersTTL),
		deleted: cache.NewTTL[struct{}](deletedUsersMax, deletedUsersTTL),
		now:     time.Now,
	}
}

// WithClock replaces the service's clock, so date rules (the birthday
// ceiling, the username cooldown, stats windows) can be tested at a fixed
// instant.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// WithAvatarStorage wires the storage client the avatar endpoints use.
// Chainable at construction: users.NewService(repo).WithAvatarStorage(sb, bucket).
func (s *Service) WithAvatarStorage(storage AvatarStorage, bucket string) *Service {
	s.storage = storage
	s.bucket = bucket
	return s
}

func (s *Service) EnsureUser(ctx context.Context, id uuid.UUID) error {
	return s.repo.EnsureUser(ctx, id)
}

func (s *Service) GetMe(ctx context.Context, id uuid.UUID) (*User, error) {
	return s.repo.GetUserByID(ctx, id)
}

// UpdateProfileRequest is the validated body for PATCH /v1/users/me.
type UpdateProfileRequest struct {
	Username *string `json:"username"`
	Bio      *string `json:"bio"`
	// Settings replaces the whole blob when present (the client owns the
	// merge). Absent means unchanged.
	Settings *json.RawMessage `json:"settings"`
	// Birthday is tri-state: absent = unchanged, null = clear, value = set.
	// Kept raw so absent and null stay distinguishable; parsed in UpdateMe.
	Birthday json.RawMessage `json:"birthday"`
	// NotifyPremieres and NotifyReleases are absent when unchanged.
	NotifyPremieres *bool `json:"notify_premieres"`
	NotifyReleases  *bool `json:"notify_releases"`
}

// UpdateMe applies a profile patch. A username, when provided, must pass
// ValidateUsername and clear the change cooldown. Other fields are passed
// through as-is — length and format constraints on those are a frontend
// concern at this stage.
//
// Collisions are not pre-checked here: the unique index is the only race-free
// answer, and the repository already translates it into ErrUsernameTaken.
func (s *Service) UpdateMe(ctx context.Context, id uuid.UUID, req UpdateProfileRequest) (*User, error) {
	if req.Username != nil {
		if err := ValidateUsername(*req.Username); err != nil {
			return nil, err
		}
		if err := s.checkUsernameCooldown(ctx, id, *req.Username); err != nil {
			return nil, err
		}
	}
	if req.Settings != nil {
		if err := validateSettings(*req.Settings); err != nil {
			return nil, err
		}
	}
	birthday, err := parseBirthday(req.Birthday, s.now())
	if err != nil {
		return nil, err
	}
	return s.repo.UpdateProfile(ctx, id, UpdateProfilePatch{
		Username: req.Username,
		Bio:      req.Bio,
		Settings: req.Settings,
		Birthday: birthday,

		NotifyPremieres: req.NotifyPremieres,
		NotifyReleases:  req.NotifyReleases,
	})
}

// GetStats aggregates the user's library and event log into the profile
// Stats payload.
func (s *Service) GetStats(ctx context.Context, id uuid.UUID) (*Stats, error) {
	rows, err := s.repo.StatsRowsByUser(ctx, id)
	if err != nil {
		return nil, err
	}
	events, err := s.repo.StatsEventsByUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return ComputeStats(rows, events, s.now()), nil
}

// checkUsernameCooldown refuses a rename that lands inside the cooldown
// window. Two cases deliberately pass straight through:
//
//   - username_changed_at is nil. The row still holds the auto-generated
//     user_<8 hex> name, which the user never chose, so claiming a real name
//     for the first time is free. This is what lets the web client apply the
//     name picked at sign-up without consuming the user's one change.
//   - The name is unchanged, ignoring case. Re-saving a profile form, or
//     fixing capitalisation, is not a rename and must not start a cooldown or
//     reject a PATCH that is really editing the bio.
func (s *Service) checkUsernameCooldown(ctx context.Context, id uuid.UUID, next string) error {
	current, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	if current == nil || current.UsernameChangedAt == nil {
		return nil
	}
	if strings.EqualFold(current.Username, next) {
		return nil
	}

	if retryAt := current.UsernameChangedAt.Add(UsernameChangeCooldown); s.now().Before(retryAt) {
		return &UsernameCooldownError{RetryAt: retryAt}
	}
	return nil
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

// EnsureUserMiddleware upserts the `users` row on a user's first authenticated
// request, then skips the round trip while the user stays in the ensured cache.
// Mount it AFTER the JWT auth middleware on protected routes — it assumes the
// user UUID is already in context. Failures are logged best-effort and do not
// reject the request, on the assumption that catalog state is more useful to
// the user than a 5xx if the upsert briefly fails. A failed upsert is retried
// on the next request. A token belonging to an account deleted a moment ago is
// refused, so it can't bring the row back.
func (s *Service) EnsureUserMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, ok := middleware.UserUUIDFromContext(r.Context()); ok {
				if _, gone := s.deleted.Get(id.String()); gone {
					response.WriteError(w, http.StatusUnauthorized, "account deleted")
					return
				}
				if _, done := s.ensured.Get(id.String()); !done {
					if err := s.repo.EnsureUser(r.Context(), id); err != nil {
						log.Printf("best-effort EnsureUser failed for %s: %v", id, err)
					} else {
						s.ensured.Set(id.String(), struct{}{})
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
