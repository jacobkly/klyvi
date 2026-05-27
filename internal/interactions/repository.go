package interactions

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// Record appends an interaction row. Interactions are append-only — the
// signal pipeline aggregates them at read time.
func (r *Repository) Record(ctx context.Context, e *Interaction) error {
	return r.db.QueryRowxContext(ctx, `
		insert into interactions (user_id, media_id, media_type, kind, rating, source)
		values ($1, $2, $3, $4, $5, $6)
		returning id, created_at
	`, e.UserID, e.MediaID, e.MediaType, e.Kind, e.Rating, e.Source).
		Scan(&e.ID, &e.CreatedAt)
}

// ListByUser returns all interactions for a user, most recent first.
// Optionally bounded to interactions within the last `sinceDays` days
// (pass 0 for "all time").
func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]Interaction, error) {
	var rows []Interaction
	if sinceDays > 0 {
		err := r.db.SelectContext(ctx, &rows, `
			select * from interactions
			where user_id = $1 and created_at >= now() - make_interval(days => $2)
			order by created_at desc
		`, userID, sinceDays)
		return rows, err
	}
	err := r.db.SelectContext(ctx, &rows, `
		select * from interactions
		where user_id = $1
		order by created_at desc
	`, userID)
	return rows, err
}

// CountByUser reports the number of interactions a user has — the cheap
// gate the orchestrator uses to choose between Tier 0 and Tier 1+.
func (r *Repository) CountByUser(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n,
		`select count(*) from interactions where user_id = $1`, userID)
	return n, err
}
