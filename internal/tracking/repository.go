package tracking

import (
	"context"
	"database/sql"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// EnsureMediaIndexAndGetID upserts a media_index row for the given TMDB id
// + media_type (+ season_number for 'season') and returns the surrogate
// media_id. The DO UPDATE clause is a no-op write that exists so RETURNING
// fires on conflict, avoiding a follow-up SELECT round-trip.
//
// seasonNumber must be non-nil when mediaType == 'season', nil otherwise.
// The UNIQUE NULLS NOT DISTINCT constraint added in migration 00014 makes
// NULL season_number a valid conflict target for movies.
func (r *Repository) EnsureMediaIndexAndGetID(ctx context.Context, tmdbID int, mediaType string, seasonNumber *int) (int, error) {
	var mediaID int
	err := r.db.QueryRowxContext(ctx, `
		insert into media_index (id, season_number, media_type)
		values ($1, $2, $3)
		on conflict (id, season_number, media_type)
		do update set media_type = excluded.media_type
		returning media_id
	`, tmdbID, seasonNumber, mediaType).Scan(&mediaID)
	if err != nil {
		return 0, err
	}
	return mediaID, nil
}

// UpdatePatch carries optional updates. A nil pointer means "do not change".
// Clearing a field back to NULL is not supported through this surface —
// callers either pass a new value or leave the existing one in place.
type UpdatePatch struct {
	Status          *string
	Score           *int
	EpisodeProgress *int
	Notes           *string
}

// ListFilters narrows the result set for the user-facing list endpoint.
type ListFilters struct {
	MediaType *string
	Status    *string
}

// entrySelectClause is the column list every read returns — media_list
// columns plus catalog display fields joined from movies / tv_series /
// tv_seasons. For movie rows the movies-side columns populate; for
// season rows the tv-side columns populate. COALESCE picks the right
// value per media_type because the unmatched LEFT JOIN side is NULL.
//
// `season poster, fallback to series poster` is realised via the
// COALESCE order: tvse.poster_path then tvs.poster_path.
const entrySelectClause = `
	ml.id, ml.user_id, ml.media_id, ml.media_type,
	ml.status, ml.score, ml.episode_progress,
	ml.start_date, ml.finish_date,
	ml.total_rewatches, ml.notes, ml.is_deleted,
	ml.created_at, ml.updated_at,
	coalesce(m.movie_id, tvs.tv_id) as tmdb_id,
	coalesce(m.title, tvs.name, tvs.original_name) as title,
	coalesce(m.poster_path, tvse.poster_path, tvs.poster_path) as poster_path,
	coalesce(m.backdrop_path, tvs.backdrop_path) as backdrop_path,
	coalesce(
		extract(year from m.release_date)::int,
		extract(year from tvse.air_date)::int,
		extract(year from tvs.first_air_date)::int
	) as release_year,
	tvse.season_number as season_number,
	tvse.name as season_name
`

// entryFromClause is the LEFT JOIN chain that produces a row per
// media_list entry with the right catalog columns populated.
const entryFromClause = `
	from media_list ml
	left join media_index mi on mi.media_id = ml.media_id
	left join movies m on ml.media_type = 'movie' and m.movie_id = mi.id
	left join tv_series tvs on ml.media_type = 'season' and tvs.tv_id = mi.id
	left join tv_seasons tvse
		on ml.media_type = 'season'
		and tvse.tv_id = mi.id
		and tvse.season_number = mi.season_number
`

// AddEntry inserts a new tracking row, then returns the enriched row via
// GetEntry. Idempotent: a second add for the same (user, media_id) returns
// the existing entry rather than erroring. status / score /
// episode_progress / notes can be nil to use the table defaults.
func (r *Repository) AddEntry(ctx context.Context, userID uuid.UUID, mediaID int, mediaType string, status *string, score *int, episodeProgress *int, notes *string) (*Entry, error) {
	if _, err := r.db.ExecContext(ctx, `
		insert into media_list (user_id, media_id, media_type, status, score, episode_progress, notes)
		values ($1, $2, $3, $4, $5, coalesce($6, 0), $7)
		on conflict (user_id, media_id) do nothing
	`, userID, mediaID, mediaType, status, score, episodeProgress, notes); err != nil {
		return nil, err
	}
	return r.GetEntry(ctx, userID, mediaID)
}

func (r *Repository) GetEntry(ctx context.Context, userID uuid.UUID, mediaID int) (*Entry, error) {
	var e Entry
	err := r.db.GetContext(ctx, &e, `
		select `+entrySelectClause+entryFromClause+`
		where ml.user_id = $1 and ml.media_id = $2
	`, userID, mediaID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &e, err
}

// UpdateEntry applies a partial patch using COALESCE — only non-nil fields
// in the patch overwrite the existing row. Returns the updated entry, or
// nil if no row matched (user_id, media_id).
func (r *Repository) UpdateEntry(ctx context.Context, userID uuid.UUID, mediaID int, patch UpdatePatch) (*Entry, error) {
	res, err := r.db.ExecContext(ctx, `
		update media_list set
			status = coalesce($3, status),
			score = coalesce($4, score),
			episode_progress = coalesce($5, episode_progress),
			notes = coalesce($6, notes),
			updated_at = now()
		where user_id = $1 and media_id = $2
	`, userID, mediaID, patch.Status, patch.Score, patch.EpisodeProgress, patch.Notes)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	return r.GetEntry(ctx, userID, mediaID)
}

// DeleteEntry hard-deletes the row. The is_deleted column is left in the
// schema for a future soft-delete / undo feature, but is not used yet.
func (r *Repository) DeleteEntry(ctx context.Context, userID uuid.UUID, mediaID int) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		delete from media_list where user_id = $1 and media_id = $2
	`, userID, mediaID)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// ListByUser returns the tracking entries for a user, filterable by media_type
// and status. Most recently touched first. The JOIN populates display
// fields so the frontend can render directly off this without per-row
// follow-up lookups.
func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID, filters ListFilters) ([]Entry, error) {
	var sb strings.Builder
	sb.WriteString(`select `)
	sb.WriteString(entrySelectClause)
	sb.WriteString(entryFromClause)
	sb.WriteString(` where ml.user_id = $1`)
	args := []any{userID}

	if filters.MediaType != nil {
		args = append(args, *filters.MediaType)
		sb.WriteString(" and ml.media_type = $")
		sb.WriteString(itoa(len(args)))
	}
	if filters.Status != nil {
		args = append(args, *filters.Status)
		sb.WriteString(" and ml.status = $")
		sb.WriteString(itoa(len(args)))
	}
	sb.WriteString(" order by ml.updated_at desc")

	var entries []Entry
	if err := r.db.SelectContext(ctx, &entries, sb.String(), args...); err != nil {
		return nil, err
	}
	return entries, nil
}

// itoa is a tiny helper to avoid pulling strconv just for $N placeholders.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	// Two-digit fallback; the filter list above never produces more than a
	// handful of placeholders, so this is defensive only.
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
