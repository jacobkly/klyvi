package movies

import (
	"context"
	"database/sql"

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
