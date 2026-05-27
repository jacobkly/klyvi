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

func defaultUsername(id uuid.UUID) string {
	s := id.String()
	return fmt.Sprintf("user_%s", s[:8])
}
