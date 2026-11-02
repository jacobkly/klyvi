package imports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

const uniqueViolation = "23505"

// staleMessage is the reason recorded on a job whose worker stopped.
const staleMessage = "import did not complete (worker stopped)"

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// activeImportIndex is the partial unique index that allows one pending or
// running import per user.
const activeImportIndex = "imports_one_active_per_user"

// Create inserts a pending job and returns it, or ErrImportActive when the
// user already has one pending or running. A running job Get would report as
// stale is failed first, so the retry Get invites is not refused by it.
func (r *Repository) Create(ctx context.Context, userID uuid.UUID, source string) (*Import, error) {
	if _, err := r.db.ExecContext(ctx, `
		update imports set status = 'failed', error = $2, updated_at = now()
		where user_id = $1 and status = 'running' and updated_at < $3
	`, userID, staleMessage, time.Now().Add(-staleAfter)); err != nil {
		return nil, fmt.Errorf("fail stale import: %w", err)
	}

	var imp Import
	err := r.db.GetContext(ctx, &imp, `
		insert into imports (id, user_id, source, status)
		values (gen_random_uuid(), $1, $2, 'pending')
		returning *
	`, userID, source)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == activeImportIndex {
			return nil, ErrImportActive
		}
		return nil, fmt.Errorf("create import: %w", err)
	}
	return &imp, nil
}

// FailUnfinished fails every pending or running job. Workers run inside the
// API process, so at startup any such job belongs to a process that has
// stopped and will never move. This assumes one API instance, which is how
// it is deployed.
func (r *Repository) FailUnfinished(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		update imports set status = 'failed', error = $1, updated_at = now()
		where status in ('pending', 'running')
	`, staleMessage)
	if err != nil {
		return fmt.Errorf("fail unfinished imports: %w", err)
	}
	return nil
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
		msg := staleMessage
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
