package movies

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetByTMDBID(ctx context.Context, tmdbID int) (*Movie, error) {
	var movie Movie
	err := r.db.GetContext(ctx, &movie,
		`select * from movies where movie_id = $1`, tmdbID)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &movie, err
}

func (r *Repository) GetMovieIDByMediaID(ctx context.Context, mediaID int) (int, error) {
	var movieID int

	err := r.db.GetContext(ctx, &movieID, `
		select id
		from media_index
		where media_id = $1 and media_type = 'movie'
	`, mediaID)

	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	return movieID, nil
}

func (r *Repository) InsertMovie(ctx context.Context, movie *Movie) error {
	_, err := r.db.NamedExecContext(ctx, `
		insert into movies (
			movie_id, adult, backdrop_path, belongs_to_collection,
			budget, genres, homepage, imdb_id,
			original_language, original_title, overview,
			popularity, poster_path, production_companies,
			production_countries, release_date, revenue,
			runtime, spoken_languages, status,
			tagline, title, video, vote_average, vote_count,
			keywords, credits
		) values (
			:movie_id, :adult, :backdrop_path, :belongs_to_collection,
			:budget, :genres, :homepage, :imdb_id,
			:original_language, :original_title, :overview,
			:popularity, :poster_path, :production_companies,
			:production_countries, :release_date, :revenue,
			:runtime, :spoken_languages, :status,
			:tagline, :title, :video, :vote_average, :vote_count,
			:keywords, :credits
		)
		on conflict (movie_id) do nothing
	`, movie)

	return err
}

func (r *Repository) EnsureMediaIndex(ctx context.Context, movieID int) error {
	_, err := r.db.ExecContext(ctx, `
		insert into media_index (id, media_type)
		values ($1, 'movie')
		on conflict (id, season_number, media_type) do nothing
	`, movieID)

	return err
}

func (r *Repository) GetCollectionIDByMovieID(ctx context.Context, movieID int) (int, error) {
	var collectionID int

	err := r.db.GetContext(ctx, &collectionID, `
		select collection_id
		from movie_collections
		where movie_id = $1
		limit 1
	`, movieID)

	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	return collectionID, nil
}

func (r *Repository) GetCollectionByCollectionID(
	ctx context.Context,
	collectionID int,
) ([]MovieCollection, error) {
	var movies []MovieCollection

	err := r.db.SelectContext(ctx, &movies, `
		select *
		from movie_collections
		where collection_id = $1
		order by position asc nulls last
	`, collectionID)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	return movies, err
}

// RecoCandidateRow is a flat projection of the movies cache for the
// recommender. The recommender's Candidate / MediaFeatures types are
// constructed from these in an adapter in cmd/api.
type RecoCandidateRow struct {
	MovieID     int              `db:"movie_id"`
	MediaID     int              `db:"media_id"`
	Genres      *json.RawMessage `db:"genres"`
	Keywords    *json.RawMessage `db:"keywords"`
	ReleaseDate *sql.NullTime    `db:"release_date"`
	VoteAverage float64          `db:"vote_average"`
	VoteCount   int              `db:"vote_count"`
}

// CandidatesByMediaIDs returns the feed projection for a specific set of
// media_index media_ids — used to load features for items in a user's
// interaction history.
func (r *Repository) CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]RecoCandidateRow, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select m.movie_id, mi.media_id, m.genres, m.keywords, m.release_date,
		       m.vote_average, m.vote_count
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where mi.media_id = any($1)
	`, mediaIDs)
	return rows, err
}

// ListCandidatesForReco returns up to `limit` movies suitable as feed
// candidates, joined with their media_index id. Cheap query — feed
// generation is sub-second.
func (r *Repository) ListCandidatesForReco(ctx context.Context, limit int) ([]RecoCandidateRow, error) {
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select m.movie_id, mi.media_id, m.genres, m.keywords, m.release_date,
		       m.vote_average, m.vote_count
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where m.vote_count > 0
		order by m.vote_count desc
		limit $1
	`, limit)
	return rows, err
}

func (r *Repository) InsertMovieCollectionBatch(
	ctx context.Context,
	entries []MovieCollection,
) error {
	if len(entries) == 0 {
		return nil
	}

	_, err := r.db.NamedExecContext(ctx, `
		insert into movie_collections (
			collection_id,
			movie_id,
			name,
			poster_path,
			vote_average,
			position
		)
		values (
			:collection_id,
			:movie_id,
			:name,
			:poster_path,
			:vote_average,
			:position
		)
		on conflict do nothing
	`, entries)

	return err
}
