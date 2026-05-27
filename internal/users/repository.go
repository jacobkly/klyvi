package users

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

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

// UpdateProfilePatch is the partial-update shape. nil fields are unchanged.
type UpdateProfilePatch struct {
	Username  *string
	Bio       *string
	AvatarURL *string
	BannerURL *string
}

// UpdateProfile applies a partial patch to the user row using COALESCE — only
// non-nil fields in the patch overwrite. Returns the updated row, or nil if
// no row matched the id.
func (r *Repository) UpdateProfile(ctx context.Context, id uuid.UUID, patch UpdateProfilePatch) (*User, error) {
	var u User
	err := r.db.QueryRowxContext(ctx, `
		update users set
			username   = coalesce($2, username),
			bio        = coalesce($3, bio),
			avatar_url = coalesce($4, avatar_url),
			banner_url = coalesce($5, banner_url),
			updated_at = now()
		where id = $1
		returning *
	`, id, patch.Username, patch.Bio, patch.AvatarURL, patch.BannerURL).StructScan(&u)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func defaultUsername(id uuid.UUID) string {
	s := id.String()
	return fmt.Sprintf("user_%s", s[:8])
}
