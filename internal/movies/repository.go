package movies

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// movieColumns is the explicit column list for reads that scan into Movie.
// Explicit rather than `select *` so generated columns added by later
// migrations — genre_ids, keyword_ids (00027), which the Movie struct does not
// carry — do not break the struct scan.
const movieColumns = `movie_id, adult, backdrop_path, belongs_to_collection,
	budget, genres, homepage, imdb_id, original_language, original_title,
	overview, popularity, poster_path, production_companies,
	production_countries, release_date, revenue, runtime, spoken_languages,
	status, tagline, title, video, vote_average, vote_count, keywords, credits,
	created_at, updated_at`

func (r *Repository) GetByTMDBID(ctx context.Context, tmdbID int) (*Movie, error) {
	var movie Movie
	err := r.db.GetContext(ctx, &movie,
		`select `+movieColumns+` from movies where movie_id = $1`, tmdbID)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &movie, nil
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
// constructed from these in an adapter in cmd/api. Display fields
// (title, poster_path, backdrop_path) are included so the feed handler
// can return everything the frontend needs to render a card without an
// N+1 follow-up lookup.
type RecoCandidateRow struct {
	MovieID      int              `db:"movie_id"`
	MediaID      int              `db:"media_id"`
	Title        *string          `db:"title"`
	PosterPath   *string          `db:"poster_path"`
	BackdropPath *string          `db:"backdrop_path"`
	Genres       *json.RawMessage `db:"genres"`
	Keywords     *json.RawMessage `db:"keywords"`
	ReleaseDate  *sql.NullTime    `db:"release_date"`
	VoteAverage  float64          `db:"vote_average"`
	VoteCount    int              `db:"vote_count"`
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
		select m.movie_id, mi.media_id, m.title, m.poster_path, m.backdrop_path,
		       m.genres, m.keywords, m.release_date, m.vote_average, m.vote_count
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where mi.media_id = any($1)
	`, mediaIDs)
	return rows, err
}

// LookupGenreNames returns id→name pairs for the requested genre ids by
// scanning the JSONB genres column across the movies cache. Ids that
// don't appear in any cached movie are simply absent from the returned
// map. Cheap because the query is bounded by len(ids) and most genres
// recur across the catalog.
func (r *Repository) LookupGenreNames(ctx context.Context, ids []int) (map[int]string, error) {
	return r.lookupJSONBNames(ctx, "genres", ids)
}

// LookupKeywordNames is the keywords twin of LookupGenreNames.
func (r *Repository) LookupKeywordNames(ctx context.Context, ids []int) (map[int]string, error) {
	return r.lookupJSONBNames(ctx, "keywords", ids)
}

// lookupJSONBNames scans `column` (a JSONB array of {id, name} objects)
// across the movies table and returns name strings for the requested ids.
// coalesce(column, '[]') handles null arrays without erroring.
func (r *Repository) lookupJSONBNames(ctx context.Context, column string, ids []int) (map[int]string, error) {
	if len(ids) == 0 {
		return map[int]string{}, nil
	}
	query := `
		select distinct (elem->>'id')::int as id, elem->>'name' as name
		from movies, jsonb_array_elements(coalesce(` + column + `, '[]'::jsonb)) elem
		where (elem->>'id')::int = any($1)
	`
	var rows []struct {
		ID   int    `db:"id"`
		Name string `db:"name"`
	}
	if err := r.db.SelectContext(ctx, &rows, query, ids); err != nil {
		return nil, err
	}
	out := make(map[int]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Name
	}
	return out, nil
}

// ListCandidatesForReco returns up to `limit` movies suitable as feed
// candidates, joined with their media_index id. Cheap query — feed
// generation is sub-second.
func (r *Repository) ListCandidatesForReco(ctx context.Context, limit int) ([]RecoCandidateRow, error) {
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select m.movie_id, mi.media_id, m.title, m.poster_path, m.backdrop_path,
		       m.genres, m.keywords, m.release_date, m.vote_average, m.vote_count
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where m.vote_count > 0
		order by m.vote_count desc
		limit $1
	`, limit)
	return rows, err
}

// ListCandidatesByTaste returns up to `limit` feed candidates that share at
// least one genre or keyword id with the user's taste, above the vote floor.
// Keyword-overlapping rows come first — keyword overlap is the recommender's
// differentiator, so those candidates must not be crowded out by high-vote
// genre-only matches — then genre-overlapping rows fill the rest. Both are
// ordered by vote_count so the pool favours well-regarded titles; Tier 1
// re-ranks precisely, this only assembles the pool.
//
// Two GIN-indexed queries, each with a plain vote_count sort, rather than one
// query with an `order by (keyword_ids && $arr)` recheck: that recheck runs a
// large array overlap for every matched row and becomes pathologically slow
// for a user whose taste carries hundreds of keywords.
func (r *Repository) ListCandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]RecoCandidateRow, error) {
	seen := make(map[int]bool)
	out := make([]RecoCandidateRow, 0, limit)
	add := func(rows []RecoCandidateRow) {
		for _, row := range rows {
			if len(out) >= limit {
				return
			}
			if !seen[row.MediaID] {
				seen[row.MediaID] = true
				out = append(out, row)
			}
		}
	}

	if len(keywordIDs) > 0 {
		rows, err := r.candidatesByOverlap(ctx, "keyword_ids", keywordIDs, minVotes, limit)
		if err != nil {
			return nil, err
		}
		add(rows)
	}
	if len(out) < limit && len(genreIDs) > 0 {
		rows, err := r.candidatesByOverlap(ctx, "genre_ids", genreIDs, minVotes, limit)
		if err != nil {
			return nil, err
		}
		add(rows)
	}
	return out, nil
}

// candidatesByOverlap returns up to `limit` movies whose `column` (an int[]
// with a GIN index — genre_ids or keyword_ids) overlaps ids, above the vote
// floor, most-voted first. column is a fixed internal identifier, never user
// input.
func (r *Repository) candidatesByOverlap(ctx context.Context, column string, ids []int, minVotes, limit int) ([]RecoCandidateRow, error) {
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select m.movie_id, mi.media_id, m.title, m.poster_path, m.backdrop_path,
		       m.genres, m.keywords, m.release_date, m.vote_average, m.vote_count
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where m.vote_count >= $1 and m.`+column+` && $2::int[]
		order by m.vote_count desc
		limit $3
	`, minVotes, ids, limit)
	return rows, err
}

// RefreshFeatureStats recomputes the movie feature-frequency tables that back
// the recommender's inverse-document-frequency weighting. Over the eligible
// catalog (vote_count > 0) it counts how many movies carry each genre and
// keyword id — reading the generated genre_ids / keyword_ids arrays (00027) —
// and records the corpus size N. The whole recompute runs in one transaction
// so a concurrent reader never sees a half-rebuilt table.
//
// Only the media_type='movie' rows are touched; TV rows (when TV reco lands)
// refresh independently under their own media_type.
func (r *Repository) RefreshFeatureStats(ctx context.Context) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`delete from feature_stats where media_type = 'movie'`); err != nil {
		return fmt.Errorf("clear feature_stats: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		insert into feature_stats (media_type, feature_type, feature_id, doc_count)
		select 'movie', 'genre', g, count(*)
		from movies, unnest(genre_ids) g
		where vote_count > 0
		group by g
	`); err != nil {
		return fmt.Errorf("count genres: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		insert into feature_stats (media_type, feature_type, feature_id, doc_count)
		select 'movie', 'keyword', k, count(*)
		from movies, unnest(keyword_ids) k
		where vote_count > 0
		group by k
	`); err != nil {
		return fmt.Errorf("count keywords: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		insert into feature_corpus (media_type, doc_count, updated_at)
		values ('movie', (select count(*) from movies where vote_count > 0), now())
		on conflict (media_type) do update
			set doc_count = excluded.doc_count, updated_at = now()
	`); err != nil {
		return fmt.Errorf("count corpus: %w", err)
	}

	return tx.Commit()
}

// LoadFeatureStats loads the recommender's IDF inputs for a media type: the
// per-feature document frequencies (genre and keyword) and the corpus size N,
// as written by RefreshFeatureStats. A zero corpus means the stats have not
// been refreshed yet, and the caller runs without IDF weighting.
func (r *Repository) LoadFeatureStats(ctx context.Context, mediaType string) (genreDF, keywordDF map[int]int, corpus int, err error) {
	var rows []struct {
		FeatureType string `db:"feature_type"`
		FeatureID   int    `db:"feature_id"`
		DocCount    int    `db:"doc_count"`
	}
	if err = r.db.SelectContext(ctx, &rows, `
		select feature_type, feature_id, doc_count
		from feature_stats
		where media_type = $1
	`, mediaType); err != nil {
		return nil, nil, 0, fmt.Errorf("select feature_stats: %w", err)
	}

	genreDF = make(map[int]int)
	keywordDF = make(map[int]int)
	for _, row := range rows {
		switch row.FeatureType {
		case "genre":
			genreDF[row.FeatureID] = row.DocCount
		case "keyword":
			keywordDF[row.FeatureID] = row.DocCount
		}
	}

	err = r.db.GetContext(ctx, &corpus,
		`select doc_count from feature_corpus where media_type = $1`, mediaType)
	if err == sql.ErrNoRows {
		return genreDF, keywordDF, 0, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("select feature_corpus: %w", err)
	}
	return genreDF, keywordDF, corpus, nil
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
