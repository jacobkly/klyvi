package onboarding

import (
	"context"

	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// ListActive returns up to `limit` active pool entries ordered by
// `display_order` ascending. limit <= 0 means "no limit".
func (r *Repository) ListActive(ctx context.Context, limit int) ([]PoolEntry, error) {
	var rows []PoolEntry
	if limit > 0 {
		err := r.db.SelectContext(ctx, &rows, `
			select tmdb_id, dimension, display_order, active, added_at
			from onboarding_pool
			where active = true
			order by display_order asc, tmdb_id asc
			limit $1
		`, limit)
		return rows, err
	}
	err := r.db.SelectContext(ctx, &rows, `
		select tmdb_id, dimension, display_order, active, added_at
		from onboarding_pool
		where active = true
		order by display_order asc, tmdb_id asc
	`)
	return rows, err
}

// Upsert inserts or updates an entry, keyed by tmdb_id. The seed binary
// uses this so re-runs are safe.
func (r *Repository) Upsert(ctx context.Context, e PoolEntry) error {
	_, err := r.db.ExecContext(ctx, `
		insert into onboarding_pool (tmdb_id, dimension, display_order, active)
		values ($1, $2, $3, $4)
		on conflict (tmdb_id) do update set
			dimension     = excluded.dimension,
			display_order = excluded.display_order,
			active        = excluded.active
	`, e.TMDBID, e.Dimension, e.DisplayOrder, e.Active)
	return err
}
