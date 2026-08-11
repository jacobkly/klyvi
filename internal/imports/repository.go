package imports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a pending job and returns it.
func (r *Repository) Create(ctx context.Context, userID uuid.UUID, source string) (*Import, error) {
	var imp Import
	err := r.db.GetContext(ctx, &imp, `
		insert into imports (id, user_id, source, status)
		values (gen_random_uuid(), $1, $2, 'pending')
		returning *
	`, userID, source)
	if err != nil {
		return nil, fmt.Errorf("create import: %w", err)
	}
	return &imp, nil
}

// Get returns a job scoped to its owner. A 'running' job whose updated_at has
// gone stale is reported as failed — the in-process worker died with the
// server, so this is how a client stops polling a job that will never move.
func (r *Repository) Get(ctx context.Context, userID, id uuid.UUID) (*Import, error) {
	var imp Import
	err := r.db.GetContext(ctx, &imp, `
		select * from imports where id = $1 and user_id = $2
	`, id, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get import: %w", err)
	}

	if imp.Status == StatusRunning && time.Since(imp.UpdatedAt) > staleAfter {
		imp.Status = StatusFailed
		msg := "import did not complete (worker stopped)"
		imp.Error = &msg
	}
	return &imp, nil
}

// SetStatus moves a job to a new status, optionally recording an error.
func (r *Repository) SetStatus(ctx context.Context, id uuid.UUID, status string, errMsg *string) error {
	_, err := r.db.ExecContext(ctx, `
		update imports set status = $2, error = $3, updated_at = now()
		where id = $1
	`, id, status, errMsg)
	if err != nil {
		return fmt.Errorf("set import status: %w", err)
	}
	return nil
}

// SetProgress updates the running counts. Called as the worker advances so a
// poller sees movement and staleness detection stays accurate.
func (r *Repository) SetProgress(ctx context.Context, id uuid.UUID, total, matched, unmatched int) error {
	_, err := r.db.ExecContext(ctx, `
		update imports set total = $2, matched = $3, unmatched = $4, updated_at = now()
		where id = $1
	`, id, total, matched, unmatched)
	if err != nil {
		return fmt.Errorf("set import progress: %w", err)
	}
	return nil
}

// SetUnmatchedTitles records the titles a job could not resolve. Called once
// at the end — the full set is only known when processing finishes.
func (r *Repository) SetUnmatchedTitles(ctx context.Context, id uuid.UUID, titles UnmatchedTitles) error {
	_, err := r.db.ExecContext(ctx, `
		update imports set unmatched_titles = $2, updated_at = now()
		where id = $1
	`, id, titles)
	if err != nil {
		return fmt.Errorf("set import unmatched titles: %w", err)
	}
	return nil
}
