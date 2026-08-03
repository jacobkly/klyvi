package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// settingsArg converts the optional settings blob to a driver-friendly
// value: nil pointer → SQL NULL (COALESCE keeps the stored blob), non-nil →
// the raw bytes for the JSONB write.
func settingsArg(s *json.RawMessage) any {
	if s == nil {
		return nil
	}
	return []byte(*s)
}

// uniqueViolation is the SQLSTATE Postgres raises when an insert or update
// collides with a unique index.
const uniqueViolation = "23505"

// usernameUniqueIndex is the index created in migration 00022. Matching on
// its name keeps an unrelated future unique violation from being reported to
// the user as a username collision.
const usernameUniqueIndex = "users_username_lower_key"

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// EnsureUser inserts a row for the given Supabase user UUID if one does not
// already exist. Username gets a deterministic sentinel ("user_<8>") because
// the column is NOT NULL UNIQUE; the user can rename later. ON CONFLICT
// (id) DO NOTHING makes this safe to call on every authenticated request.
func (r *Repository) EnsureUser(ctx context.Context, id uuid.UUID) error {
	username := defaultUsername(id)
	_, err := r.db.ExecContext(ctx, `
		insert into users (id, username)
		values ($1, $2)
		on conflict (id) do nothing
	`, id, username)
	return err
}

func (r *Repository) GetUserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := r.db.GetContext(ctx, &u,
		`select * from users where id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

// ClearAvatarURL nulls avatar_url — the COALESCE patch path cannot express
// "set to NULL", so the avatar delete flow gets its own statement.
func (r *Repository) ClearAvatarURL(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := r.db.QueryRowxContext(ctx, `
		update users set avatar_url = null, updated_at = now()
		where id = $1
		returning *
	`, id).StructScan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("clear avatar url: %w", err)
	}
	return &u, nil
}

// UsernameExists reports whether a username is already held, compared
// case-insensitively so it agrees with the users_username_lower_key index.
func (r *Repository) UsernameExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := r.db.QueryRowxContext(ctx, `
		select exists(select 1 from users where lower(username) = lower($1))
	`, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("username exists check: %w", err)
	}
	return exists, nil
}

// UpdateProfilePatch is the partial-update shape. nil fields are unchanged.
type UpdateProfilePatch struct {
	Username  *string
	Bio       *string
	AvatarURL *string
	BannerURL *string
	// Settings replaces the whole JSONB blob when non-nil.
	Settings *json.RawMessage
	// Birthday is tri-state — see OptionalDate.
	Birthday OptionalDate
}

// UpdateProfile applies a partial patch to the user row using COALESCE — only
// non-nil fields in the patch overwrite. Returns the updated row, or nil if
// no row matched the id.
//
// username_changed_at is stamped in the same statement that writes the name,
// so the two can never drift. The lower() comparison reads the pre-update
// value, which means re-saving the same name (or only its capitalisation)
// does not count as a change and does not start a cooldown.
//
// A concurrent update that loses the race on users_username_lower_key comes
// back as ErrUsernameTaken rather than a driver error, so the caller does not
// have to know what SQLSTATE 23505 is.
func (r *Repository) UpdateProfile(ctx context.Context, id uuid.UUID, patch UpdateProfilePatch) (*User, error) {
	var u User
	err := r.db.QueryRowxContext(ctx, `
		update users set
			username            = coalesce($2, username),
			username_changed_at = case
				when $2 is not null and lower($2) <> lower(username) then now()
				else username_changed_at
			end,
			bio        = coalesce($3, bio),
			avatar_url = coalesce($4, avatar_url),
			banner_url = coalesce($5, banner_url),
			settings   = coalesce($6, settings),
			birthday   = case when $7 then $8 else birthday end,
			updated_at = now()
		where id = $1
		returning *
	`, id, patch.Username, patch.Bio, patch.AvatarURL, patch.BannerURL, settingsArg(patch.Settings),
		patch.Birthday.Set, patch.Birthday.Value).StructScan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		if isUsernameTaken(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("update profile: %w", err)
	}
	return &u, nil
}

// isUsernameTaken reports whether err is a unique violation on the username
// index specifically.
func isUsernameTaken(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == uniqueViolation && pgErr.ConstraintName == usernameUniqueIndex
}

func defaultUsername(id uuid.UUID) string {
	s := id.String()
	return fmt.Sprintf("user_%s", s[:8])
}
