package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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
	Favorite        *bool
}

// AddParams is the insert shape for a new tracking entry. Optional fields
// nil out to the table defaults.
type AddParams struct {
	MediaType       string
	Status          *string
	Score           *int
	EpisodeProgress *int
	Notes           *string
	Favorite        *bool
}

// ListFilters narrows the result set for the user-facing list endpoint.
// Limit 0 means the whole list. After, when set, starts the page just past
// that row in the list's order.
type ListFilters struct {
	MediaType *string
	Status    *string
	Limit     int
	After     *Cursor
}

// entrySelectClause is the column list every read returns — media_list
// columns plus catalog display fields joined from movies / tv_series /
// tv_seasons. For movie rows the movies-side columns populate; for
// season rows the tv-side columns populate. COALESCE picks the right
// value per media_type because the unmatched LEFT JOIN side is NULL.
//
// `season poster, fallback to series poster` is realised via the
// COALESCE order: tvse.poster_path then tvs.poster_path.
//
// season_number comes from media_index, the season's identity, so it is
// set even before the season is cached in tv_seasons.
const entrySelectClause = `
	ml.id, ml.user_id, ml.media_id, ml.media_type,
	ml.status, ml.score, ml.episode_progress,
	ml.start_date, ml.finish_date,
	ml.total_rewatches, ml.notes, ml.favorite, ml.is_deleted,
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
	mi.season_number as season_number,
	tvse.name as season_name,
	case when jsonb_typeof(tvse.episodes) = 'array' then jsonb_array_length(tvse.episodes) end as episode_count
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
// the existing entry rather than erroring. Optional params nil out to the
// table defaults.
func (r *Repository) AddEntry(ctx context.Context, userID uuid.UUID, mediaID int, p AddParams) (*Entry, error) {
	if _, err := r.db.ExecContext(ctx, `
		insert into media_list (user_id, media_id, media_type, status, score, episode_progress, notes, favorite)
		values ($1, $2, $3, $4, $5, coalesce($6, 0), $7, coalesce($8, false))
		on conflict (user_id, media_id) do nothing
	`, userID, mediaID, p.MediaType, p.Status, p.Score, p.EpisodeProgress, p.Notes, p.Favorite); err != nil {
		return nil, err
	}
	return r.GetEntry(ctx, userID, mediaID)
}

// MarkSeen files a title the user says they have seen as Completed with no
// score. A title already in the library keeps its entry as it is.
func (r *Repository) MarkSeen(ctx context.Context, userID uuid.UUID, mediaID int, mediaType string) error {
	_, err := r.db.ExecContext(ctx, `
		insert into media_list (user_id, media_id, media_type, status)
		values ($1, $2, $3, 'completed')
		on conflict (user_id, media_id) do nothing
	`, userID, mediaID, mediaType)
	if err != nil {
		return fmt.Errorf("mark seen: %w", err)
	}
	return nil
}

// BackfillResult counts what a backfill wrote, or would write on a dry run.
type BackfillResult struct {
	Entries  int64 `db:"entries"`
	Accounts int64 `db:"accounts"`
}

// onboardingSeenRows is every onboarding answer whose title is not in the
// answering user's library yet, once per title. $1 narrows it to one user.
const onboardingSeenRows = `
	select distinct i.user_id, i.media_id, i.media_type
	from interactions i
	where i.source = 'onboarding' and i.kind = 'rated' and i.media_type <> 'tv'
	  and ($1::uuid is null or i.user_id = $1::uuid)
	  and not exists (
		select 1 from media_list ml
		where ml.user_id = i.user_id and ml.media_id = i.media_id
	  )
`

// BackfillOnboardingSeen files past onboarding answers the way MarkSeen files
// new ones: Completed, with no score, and never over an existing entry. A nil
// userID covers every account. A dry run counts without writing.
func (r *Repository) BackfillOnboardingSeen(ctx context.Context, userID *uuid.UUID, dryRun bool) (BackfillResult, error) {
	var res BackfillResult
	query := `
		with ins as (
			insert into media_list (user_id, media_id, media_type, status)
			select user_id, media_id, media_type, 'completed' from (` + onboardingSeenRows + `) seen
			on conflict (user_id, media_id) do nothing
			returning user_id
		)
		select count(*) as entries, count(distinct user_id) as accounts from ins
	`
	if dryRun {
		query = `select count(*) as entries, count(distinct user_id) as accounts from (` + onboardingSeenRows + `) seen`
	}
	if err := r.db.GetContext(ctx, &res, query, userID); err != nil {
		return BackfillResult{}, fmt.Errorf("backfill onboarding answers: %w", err)
	}
	return res, nil
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

// GetEntryByTMDB finds the user's entry for one catalog item by its TMDB
// identity, in one query. seasonNumber is nil for a movie. Returns nil when
// the user does not track the item.
func (r *Repository) GetEntryByTMDB(ctx context.Context, userID uuid.UUID, tmdbID int, mediaType string, seasonNumber *int) (*Entry, error) {
	var e Entry
	err := r.db.GetContext(ctx, &e, `
		select `+entrySelectClause+entryFromClause+`
		where ml.user_id = $1 and mi.id = $2 and mi.media_type = $3
		  and mi.season_number is not distinct from $4::int
	`, userID, tmdbID, mediaType, seasonNumber)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get entry by tmdb id: %w", err)
	}
	return &e, nil
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
			favorite = coalesce($7, favorite),
			updated_at = now()
		where user_id = $1 and media_id = $2
	`, userID, mediaID, patch.Status, patch.Score, patch.EpisodeProgress, patch.Notes, patch.Favorite)
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

// ResetScoresByMediaType nulls every score in one media-type scope and
// reports how many rows changed. Rows whose score is already NULL are not
// counted — "affected" answers "how many scores did I just erase".
func (r *Repository) ResetScoresByMediaType(ctx context.Context, userID uuid.UUID, mediaType string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		update media_list set score = null, updated_at = now()
		where user_id = $1 and media_type = $2 and score is not null
	`, userID, mediaType)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteByMediaType removes every tracking row in one media-type scope and
// reports how many went.
func (r *Repository) DeleteByMediaType(ctx context.Context, userID uuid.UUID, mediaType string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		delete from media_list where user_id = $1 and media_type = $2
	`, userID, mediaType)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListByUser returns the tracking entries for a user, filterable by media_type
// and status. Most recently touched first, paged by keyset when the filters
// carry a limit or a cursor. The JOIN populates display
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
		sb.WriteString(strconv.Itoa(len(args)))
	}
	if filters.Status != nil {
		args = append(args, *filters.Status)
		sb.WriteString(" and ml.status = $")
		sb.WriteString(strconv.Itoa(len(args)))
	}
	if filters.After != nil {
		args = append(args, filters.After.UpdatedAt, filters.After.ID)
		sb.WriteString(" and (ml.updated_at, ml.id) < ($")
		sb.WriteString(strconv.Itoa(len(args) - 1))
		sb.WriteString(", $")
		sb.WriteString(strconv.Itoa(len(args)))
		sb.WriteString(")")
	}
	// id breaks ties so a page boundary between rows with the same
	// updated_at neither skips nor repeats one.
	sb.WriteString(" order by ml.updated_at desc, ml.id desc")
	if filters.Limit > 0 {
		args = append(args, filters.Limit)
		sb.WriteString(" limit $")
		sb.WriteString(strconv.Itoa(len(args)))
	}

	var entries []Entry
	if err := r.db.SelectContext(ctx, &entries, sb.String(), args...); err != nil {
		return nil, err
	}
	return entries, nil
}

// EpisodeCount is the number of episodes in the season behind mediaID, or nil
// when it is a movie, unknown, or its season isn't cached yet.
func (r *Repository) EpisodeCount(ctx context.Context, mediaID int) (*int, error) {
	var n *int
	err := r.db.GetContext(ctx, &n, `
		select case when jsonb_typeof(tvse.episodes) = 'array' then jsonb_array_length(tvse.episodes) end
		from media_index mi
		left join tv_seasons tvse
			on mi.media_type = 'season'
			and tvse.tv_id = mi.id
			and tvse.season_number = mi.season_number
		where mi.media_id = $1
	`, mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("episode count: %w", err)
	}
	return n, nil
}
