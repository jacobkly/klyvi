package rankings

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Repository resolves TMDB ids to internal media_index ids. Read-only over
// media_index; it never writes, so a ranking lookup can never create spine
// rows as a side effect.
type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// ResolveMovieMediaIDs returns tmdb id → media_id for the movie index rows
// that exist, skipping ids not yet cached. Empty input returns an empty map.
func (r *Repository) ResolveMovieMediaIDs(ctx context.Context, tmdbIDs []int) (map[int]int, error) {
	out := make(map[int]int, len(tmdbIDs))
	if len(tmdbIDs) == 0 {
		return out, nil
	}

	query, args, err := sqlx.In(`
		select id, media_id from media_index
		where media_type = 'movie' and season_number is null and id in (?)
	`, tmdbIDs)
	if err != nil {
		return nil, fmt.Errorf("build resolve query: %w", err)
	}
	query = r.db.Rebind(query)

	rows, err := r.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve media ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tmdbID, mediaID int
		if err := rows.Scan(&tmdbID, &mediaID); err != nil {
			return nil, fmt.Errorf("scan media id: %w", err)
		}
		out[tmdbID] = mediaID
	}
	return out, rows.Err()
}
