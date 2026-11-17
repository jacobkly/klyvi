// Package coliked counts the titles Klyvi users like together, for the
// recommender's "people who liked this also liked" term. A nightly job
// rebuilds the counts from interactions, and the recommender reads them.
package coliked

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/reco"
)

// Options shape a rebuild.
type Options struct {
	// LikedRating is the lowest rating, 0 to 100, that counts as liking a
	// title. A title logged and never rated counts as liked.
	LikedRating int
	// UserCap keeps only each user's most recent liked titles, so one huge
	// library cannot dominate the counts or blow up the pair count.
	UserCap int
	// MinUsers drops pairs fewer users than this liked together.
	MinUsers int
	// Shrink is the k in n/(n+k) the neighbours are ranked by, so a pair many
	// users share outranks a lucky pair with the same cosine.
	Shrink float64
	// PerTitle keeps this many neighbours for each title.
	PerTitle int
}

// DefaultOptions are the rebuild settings the nightly job runs with.
func DefaultOptions() Options {
	return Options{LikedRating: 70, UserCap: 500, MinUsers: 2, Shrink: 10, PerTitle: 50}
}

// Repository owns the SQL for the co_liked table.
type Repository struct {
	db *sqlx.DB
}

// NewRepository builds a Repository on an open connection.
func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// rebuildSQL counts every pair of titles liked by the same users. A season
// counts as its series, the mean of its seasons' latest ratings deciding
// whether the series is liked. Interactions of other kinds never count.
const rebuildSQL = `
with signal as (
	select i.user_id, i.media_id as raw_id,
	       coalesce(series.media_id, i.media_id) as media_id,
	       i.kind, i.rating, i.created_at
	  from interactions i
	  left join media_index season on season.media_id = i.media_id and season.media_type = 'season'
	  left join media_index series on series.id = season.id and series.media_type = 'tv'
	 where i.kind in ('rated', 'logged')
),
per_raw as (
	select user_id, media_id,
	       (array_agg(rating order by created_at desc) filter (where kind = 'rated' and rating is not null))[1] as rating,
	       bool_or(kind = 'logged') as logged,
	       max(created_at) as last_at
	  from signal
	 group by user_id, media_id, raw_id
),
liked as (
	select user_id, media_id, max(last_at) as last_at
	  from per_raw
	 group by user_id, media_id
	having coalesce(avg(rating) >= $1::int, bool_or(logged))
),
capped as (
	select user_id, media_id
	  from (select user_id, media_id,
	               row_number() over (partition by user_id order by last_at desc, media_id) as n
	          from liked) l
	 where n <= $2::int
),
likers as (
	select media_id, count(*) as n from capped group by media_id
),
pairs as (
	select a.media_id, b.media_id as other_id, count(*) as users
	  from capped a
	  join capped b on b.user_id = a.user_id and b.media_id <> a.media_id
	 group by a.media_id, b.media_id
	having count(*) >= $3::int
),
scored as (
	select p.media_id, p.other_id, p.users,
	       p.users / sqrt(la.n::float8 * lb.n::float8) as similarity
	  from pairs p
	  join likers la on la.media_id = p.media_id
	  join likers lb on lb.media_id = p.other_id
),
ranked as (
	select media_id, other_id, users, similarity,
	       row_number() over (partition by media_id
	                          order by similarity * users / (users + $4::float8) desc, other_id) as r
	  from scored
)
insert into co_liked (media_id, other_id, users, similarity)
select media_id, other_id, users, similarity from ranked where r <= $5::int`

// Rebuild replaces every count with a fresh one from interactions, in one
// transaction so the recommender never reads a half-built table. It returns
// how many pairs it stored.
func (r *Repository) Rebuild(ctx context.Context, o Options) (int64, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("co-liked rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `delete from co_liked`); err != nil {
		return 0, fmt.Errorf("co-liked clear: %w", err)
	}
	res, err := tx.ExecContext(ctx, rebuildSQL, o.LikedRating, o.UserCap, o.MinUsers, o.Shrink, o.PerTitle)
	if err != nil {
		return 0, fmt.Errorf("co-liked count: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("co-liked count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("co-liked commit: %w", err)
	}
	return n, nil
}

// CoLiked reads the stored neighbours of each of the given titles. A title
// with none is absent from the map.
func (r *Repository) CoLiked(ctx context.Context, mediaIDs []int) (map[int][]reco.CoLiked, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	var rows []struct {
		MediaID    int     `db:"media_id"`
		OtherID    int     `db:"other_id"`
		Users      int     `db:"users"`
		Similarity float64 `db:"similarity"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select media_id, other_id, users, similarity
		  from co_liked
		 where media_id = any($1)
		 order by media_id, other_id`, mediaIDs); err != nil {
		return nil, fmt.Errorf("co-liked read: %w", err)
	}
	out := make(map[int][]reco.CoLiked, len(rows))
	for _, row := range rows {
		out[row.MediaID] = append(out[row.MediaID], reco.CoLiked{ID: row.OtherID, Users: row.Users, Sim: row.Similarity})
	}
	return out, nil
}
