// Package family finds the titles that share a Wikidata series or media
// franchise with a film or series: Better Call Saul and El Camino for
// Breaking Bad. TMDB's collections only group films, so this is what links a
// film to a series and back.
package family

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// MaxTitles caps one family. The largest franchises run to dozens of titles,
// and a shelf shows far fewer.
const MaxTitles = 40

// Title is one member of a family, with what a poster card needs.
type Title struct {
	MediaType   string     `db:"media_type" json:"media_type"`
	TMDBID      int        `db:"tmdb_id" json:"tmdb_id"`
	Title       string     `db:"title" json:"title"`
	PosterPath  *string    `db:"poster_path" json:"poster_path"`
	ReleaseDate *time.Time `db:"release_date" json:"release_date"`
}

// Repository owns the family query over wikidata_facts and the catalog cache.
type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// Family returns the other titles that share a series or franchise with the
// title, oldest first. Titles not in the catalog cache and adult films are
// left out. A title with no family, or no Wikidata facts, gets an empty list.
func (r *Repository) Family(ctx context.Context, mediaType string, tmdbID int) ([]Title, error) {
	out := []Title{}
	if err := r.db.SelectContext(ctx, &out, `
		with me as (
			select series || franchises as fam
			  from wikidata_facts
			 where media_type = $1 and tmdb_id = $2
		)
		select f.media_type, f.tmdb_id,
		       coalesce(m.title, t.name) as title,
		       coalesce(m.poster_path, t.poster_path) as poster_path,
		       coalesce(m.release_date, t.first_air_date)::timestamptz as release_date
		  from me
		  join wikidata_facts f on (f.series || f.franchises) && me.fam
		  left join movies m on f.media_type = 'movie' and m.movie_id = f.tmdb_id
		  left join tv_series t on f.media_type = 'tv' and t.tv_id = f.tmdb_id
		 where not (f.media_type = $1 and f.tmdb_id = $2)
		   and coalesce(m.title, t.name) is not null
		   and not coalesce(m.adult, false)
		 order by 5 nulls last, 3, f.tmdb_id
		 limit $3`, mediaType, tmdbID, MaxTitles); err != nil {
		return nil, fmt.Errorf("family of %s %d: %w", mediaType, tmdbID, err)
	}
	return out, nil
}
