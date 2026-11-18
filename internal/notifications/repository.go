package notifications

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Repository owns the SQL for the notifications table.
type Repository struct {
	db *sqlx.DB
}

// NewRepository builds a Repository on an open connection.
func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// selectNotices reads notices with their poster. A season falls back to its
// show's poster, and a title gone from the cache simply has none. The caller
// adds the ordering and any limit.
const selectNotices = `
	select n.id, n.kind, n.media_type, n.tmdb_id, n.season_number, n.title,
		coalesce(se.poster_path, s.poster_path, m.poster_path) as poster_path,
		n.event_date::text as event_date, n.link, n.created_at, n.read_at
	from notifications n
	left join movies m on n.media_type = 'movie' and m.movie_id = n.tmdb_id
	left join tv_series s on n.media_type = 'tv' and s.tv_id = n.tmdb_id
	left join tv_seasons se on n.media_type = 'tv' and se.tv_id = n.tmdb_id
		and se.season_number = n.season_number
	where n.user_id = $1
	order by n.created_at desc, n.id desc
`

// List returns up to limit notices, newest first, skipping offset.
func (r *Repository) List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]Notification, error) {
	notices := []Notification{}
	if err := r.db.SelectContext(ctx, &notices, selectNotices+` limit $2 offset $3`, userID, limit, offset); err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	return notices, nil
}

// ListAll returns every notice the user has, for the account export.
func (r *Repository) ListAll(ctx context.Context, userID uuid.UUID) ([]Notification, error) {
	notices := []Notification{}
	if err := r.db.SelectContext(ctx, &notices, selectNotices, userID); err != nil {
		return nil, fmt.Errorf("list all notifications: %w", err)
	}
	return notices, nil
}

// CountUnread returns how many of the user's notices are unread.
func (r *Repository) CountUnread(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	err := r.db.GetContext(ctx, &count,
		`select count(*) from notifications where user_id = $1 and read_at is null`, userID)
	if err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return count, nil
}

// MarkRead marks the given notices read. The user_id condition is what makes
// client-supplied ids safe: anyone else's are matched by nothing. A notice
// already read keeps the time it was first read, which the cleanup counts from.
func (r *Repository) MarkRead(ctx context.Context, userID uuid.UUID, ids []int64) error {
	_, err := r.db.ExecContext(ctx, `
		update notifications set read_at = now()
		where user_id = $1 and id = any($2) and read_at is null
	`, userID, ids)
	if err != nil {
		return fmt.Errorf("mark notifications read: %w", err)
	}
	return nil
}

// MarkAllRead marks every unread notice the user has as read.
func (r *Repository) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`update notifications set read_at = now() where user_id = $1 and read_at is null`, userID)
	if err != nil {
		return fmt.Errorf("mark all notifications read: %w", err)
	}
	return nil
}

// Notices age out so the table stays small under the database size cap. A read
// one goes after readNoticeMaxAge. Any notice goes after noticeMaxAge, because
// its title is a copy of TMDB data, which can't be kept past six months.
const (
	readNoticeMaxAge = 90 * 24 * time.Hour
	noticeMaxAge     = 170 * 24 * time.Hour
)

// DeleteExpired removes notices past their age limits and says how many went.
// The job's windows are shorter than these limits, so it can't re-create them.
func (r *Repository) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		delete from notifications where read_at < $1 or created_at < $2
	`, now.Add(-readNoticeMaxAge), now.Add(-noticeMaxAge))
	if err != nil {
		return 0, fmt.Errorf("delete expired notifications: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted notifications: %w", err)
	}
	return deleted, nil
}

// SeriesTracker is one user following one series.
type SeriesTracker struct {
	UserID          uuid.UUID `db:"user_id"`
	TVID            int       `db:"tv_id"`
	HighestSeason   int       `db:"highest_season"`
	NotifyPremieres bool      `db:"notify_premieres"`
}

// SeriesTrackers lists who follows which series. Status is kept per season, so
// the latest season a user tracks decides: dropping that one means they've left
// the series, whatever they did with the earlier ones.
func (r *Repository) SeriesTrackers(ctx context.Context) ([]SeriesTracker, error) {
	var trackers []SeriesTracker
	err := r.db.SelectContext(ctx, &trackers, `
		select user_id, tv_id, highest_season, notify_premieres from (
			select distinct on (ml.user_id, mi.id)
				ml.user_id, mi.id as tv_id, mi.season_number as highest_season,
				ml.status, u.notify_premieres
			from media_list ml
			join media_index mi on mi.media_id = ml.media_id and mi.media_type = 'season'
			join users u on u.id = ml.user_id
			where ml.media_type = 'season' and not ml.is_deleted and mi.season_number is not null
			order by ml.user_id, mi.id, mi.season_number desc
		) latest
		where status is distinct from 'dropped'
		order by tv_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list series trackers: %w", err)
	}
	return trackers, nil
}

// SeriesSeasons reads a cached series' name and TMDB's list of its seasons.
func (r *Repository) SeriesSeasons(ctx context.Context, tvID int) (string, []byte, error) {
	var row struct {
		Name    string `db:"name"`
		Seasons []byte `db:"seasons"`
	}
	err := r.db.GetContext(ctx, &row,
		`select coalesce(name, '') as name, seasons from tv_series where tv_id = $1`, tvID)
	if err != nil {
		return "", nil, fmt.Errorf("read seasons of series %d: %w", tvID, err)
	}
	return row.Name, row.Seasons, nil
}

// FilmPlanner is one user with one film on their planning list.
type FilmPlanner struct {
	UserID         uuid.UUID `db:"user_id"`
	TMDBID         int       `db:"tmdb_id"`
	NotifyReleases bool      `db:"notify_releases"`
}

// FilmPlanners lists planned films worth a look: no known date, not cached at
// all, or dated between from and to. Anything else can't come out soon, so it
// isn't worth a TMDB call tonight.
func (r *Repository) FilmPlanners(ctx context.Context, from, to string) ([]FilmPlanner, error) {
	var planners []FilmPlanner
	err := r.db.SelectContext(ctx, &planners, `
		select ml.user_id, mi.id as tmdb_id, u.notify_releases
		from media_list ml
		join media_index mi on mi.media_id = ml.media_id and mi.media_type = 'movie'
		join users u on u.id = ml.user_id
		left join movies m on m.movie_id = mi.id
		where ml.media_type = 'movie' and ml.status = 'planning' and not ml.is_deleted
			and (m.release_date is null or m.release_date between $1::date and $2::date)
		order by mi.id
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list film planners: %w", err)
	}
	return planners, nil
}

// FilmRelease reads a cached film's title and its release date as YYYY-MM-DD,
// which is empty when TMDB has no date.
func (r *Repository) FilmRelease(ctx context.Context, movieID int) (title, release string, err error) {
	var row struct {
		Title   string `db:"title"`
		Release string `db:"release"`
	}
	err = r.db.GetContext(ctx, &row, `
		select coalesce(title, '') as title, coalesce(release_date::text, '') as release
		from movies where movie_id = $1
	`, movieID)
	if err != nil {
		return "", "", fmt.Errorf("read release of film %d: %w", movieID, err)
	}
	return row.Title, row.Release, nil
}

// NewNotice is a notice about to be created.
type NewNotice struct {
	UserID       uuid.UUID
	Kind         string
	MediaType    string
	TMDBID       int
	SeasonNumber *int
	Title        string
	EventDate    string
}

// Insert creates a notice and reports whether it did. An event the user has
// already been told about creates nothing, which is what makes the job safe to
// re-run. So does a user who has gone, since an account can be deleted mid-run.
func (r *Repository) Insert(ctx context.Context, n NewNotice) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		insert into notifications (user_id, kind, media_type, tmdb_id, season_number, title, event_date)
		select id, $2::text, $3::text, $4::int, $5::int, $6::text, $7::date from users where id = $1
		on conflict do nothing
	`, n.UserID, n.Kind, n.MediaType, n.TMDBID, n.SeasonNumber, n.Title, n.EventDate)
	if err != nil {
		return false, fmt.Errorf("insert notification: %w", err)
	}
	created, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count created notifications: %w", err)
	}
	return created > 0, nil
}

// InsertPolicy gives every account one policy notice dated today and says how
// many it created. An account that already has a notice with this text gets
// nothing, so running it twice tells nobody twice.
func (r *Repository) InsertPolicy(ctx context.Context, text, link string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		insert into notifications (user_id, kind, title, link, event_date)
		select id, 'policy', $1::text, $2::text, current_date from users
		on conflict do nothing
	`, text, link)
	if err != nil {
		return 0, fmt.Errorf("insert policy notices: %w", err)
	}
	created, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count created policy notices: %w", err)
	}
	return created, nil
}
