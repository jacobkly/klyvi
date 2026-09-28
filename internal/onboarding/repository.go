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

// ListActiveWithCatalog returns up to limit active pool entries in
// display_order, each joined with the display fields of its cached movie, in
// one round trip.
func (r *Repository) ListActiveWithCatalog(ctx context.Context, limit int) ([]PoolRow, error) {
	var rows []PoolRow
	err := r.db.SelectContext(ctx, &rows, `
		select p.tmdb_id, p.dimension, m.title, m.poster_path, m.release_date,
		       m.credits is not null as detailed
		from onboarding_pool p
		left join movies m on m.movie_id = p.tmdb_id
		where p.active = true
		order by p.display_order asc, p.tmdb_id asc
		limit $1
	`, limit)
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
