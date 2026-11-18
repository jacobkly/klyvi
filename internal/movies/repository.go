package movies

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
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
	certification, trailer_key, has_extras, created_at, updated_at`

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

// upsertMovieSQL writes every column of a movie and stamps updated_at when
// the row already exists. InsertMovie narrows when it may overwrite.
const upsertMovieSQL = `
	insert into movies (
		movie_id, adult, backdrop_path, belongs_to_collection,
		budget, genres, homepage, imdb_id,
		original_language, original_title, overview,
		popularity, poster_path, production_companies,
		production_countries, release_date, revenue,
		runtime, spoken_languages, status,
		tagline, title, video, vote_average, vote_count,
		keywords, credits, certification, trailer_key, has_extras
	) values (
		:movie_id, :adult, :backdrop_path, :belongs_to_collection,
		:budget, :genres, :homepage, :imdb_id,
		:original_language, :original_title, :overview,
		:popularity, :poster_path, :production_companies,
		:production_countries, :release_date, :revenue,
		:runtime, :spoken_languages, :status,
		:tagline, :title, :video, :vote_average, :vote_count,
		:keywords, :credits, :certification, :trailer_key, :has_extras
	)
	on conflict (movie_id) do update set
		adult = excluded.adult,
		backdrop_path = excluded.backdrop_path,
		belongs_to_collection = excluded.belongs_to_collection,
		budget = excluded.budget,
		genres = excluded.genres,
		homepage = excluded.homepage,
		imdb_id = excluded.imdb_id,
		original_language = excluded.original_language,
		original_title = excluded.original_title,
		overview = excluded.overview,
		popularity = excluded.popularity,
		poster_path = excluded.poster_path,
		production_companies = excluded.production_companies,
		production_countries = excluded.production_countries,
		release_date = excluded.release_date,
		revenue = excluded.revenue,
		runtime = excluded.runtime,
		spoken_languages = excluded.spoken_languages,
		status = excluded.status,
		tagline = excluded.tagline,
		title = excluded.title,
		video = excluded.video,
		vote_average = excluded.vote_average,
		vote_count = excluded.vote_count,
		keywords = excluded.keywords,
		credits = excluded.credits,
		certification = excluded.certification,
		trailer_key = excluded.trailer_key,
		has_extras = excluded.has_extras,
		updated_at = now()`

const insertMovieSQL = upsertMovieSQL + `
	where movies.credits is null and excluded.credits is not null`

// InsertMovie caches a movie. A row cached from a search hit has no keywords
// or credits, so a full detail record replaces it. A full row is never
// replaced, least of all by a skinny search hit.
func (r *Repository) InsertMovie(ctx context.Context, movie *Movie) error {
	_, err := r.db.NamedExecContext(ctx, insertMovieSQL, movie)
	return err
}

// ReplaceMovie overwrites a cached movie with a fresh TMDB record, whatever
// the cached row holds, and marks it fresh.
func (r *Repository) ReplaceMovie(ctx context.Context, movie *Movie) error {
	_, err := r.db.NamedExecContext(ctx, upsertMovieSQL, movie)
	return err
}

// DeleteMovie drops a movie from the cache. Its media_index row stays, and
// with it anything users tracked or rated.
func (r *Repository) DeleteMovie(ctx context.Context, movieID int) error {
	_, err := r.db.ExecContext(ctx, `delete from movies where movie_id = $1`, movieID)
	return err
}

// StaleRow is a cached TMDB record due for a refresh: its TMDB id and when
// it was last written. A row never stamped counts as written at the epoch.
type StaleRow struct {
	ID        int       `db:"id"`
	UpdatedAt time.Time `db:"updated_at"`
}

// ListStaleMovies returns up to limit movies last written before cutoff,
// oldest first.
func (r *Repository) ListStaleMovies(ctx context.Context, cutoff time.Time, limit int) ([]StaleRow, error) {
	rows := []StaleRow{}
	err := r.db.SelectContext(ctx, &rows, `
		select movie_id as id, coalesce(updated_at, 'epoch') as updated_at
		from movies
		where coalesce(updated_at, 'epoch') < $1
		order by 2, 1
		limit $2
	`, cutoff, limit)
	return rows, err
}

// InsertMovies caches a batch in one statement, with the same conflict rules
// as InsertMovie. Duplicate ids are dropped first, because Postgres refuses to
// touch one row twice in a single insert.
func (r *Repository) InsertMovies(ctx context.Context, batch []*Movie) error {
	seen := make(map[int]bool, len(batch))
	unique := make([]*Movie, 0, len(batch))
	for _, m := range batch {
		if !seen[m.MovieID] {
			seen[m.MovieID] = true
			unique = append(unique, m)
		}
	}
	if len(unique) == 0 {
		return nil
	}
	_, err := r.db.NamedExecContext(ctx, insertMovieSQL, unique)
	return err
}

// SlimStoredCredits trims the stored credits of the given movies with
// tmdb.SlimCredits, for rows cached before credits were trimmed on write. It
// rewrites only the rows that lose entries and returns how many that was.
func (r *Repository) SlimStoredCredits(ctx context.Context, movieIDs []int) (int, error) {
	var rows []struct {
		ID      int    `db:"movie_id"`
		Credits []byte `db:"credits"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select movie_id, credits from movies
		where movie_id = any($1) and credits is not null
	`, movieIDs); err != nil {
		return 0, fmt.Errorf("read credits: %w", err)
	}

	ids := make([]int, 0, len(rows))
	slimmed := make([]string, 0, len(rows))
	for _, row := range rows {
		out, changed, err := tmdb.SlimCreditsJSON(row.Credits)
		if err != nil {
			return 0, fmt.Errorf("movie %d: %w", row.ID, err)
		}
		if changed {
			ids = append(ids, row.ID)
			slimmed = append(slimmed, string(out))
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}

	if _, err := r.db.ExecContext(ctx, `
		update movies m set credits = v.credits::jsonb
		from unnest($1::int[], $2::text[]) as v(id, credits)
		where m.movie_id = v.id
	`, ids, slimmed); err != nil {
		return 0, fmt.Errorf("write credits: %w", err)
	}
	return len(ids), nil
}

func (r *Repository) EnsureMediaIndex(ctx context.Context, movieID int) error {
	_, err := r.db.ExecContext(ctx, `
		insert into media_index (id, media_type)
		values ($1, 'movie')
		on conflict (id, season_number, media_type) do nothing
	`, movieID)

	return err
}

// EnsureMediaIndexes is EnsureMediaIndex for a batch, in one statement.
func (r *Repository) EnsureMediaIndexes(ctx context.Context, movieIDs []int) error {
	if len(movieIDs) == 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
		insert into media_index (id, media_type)
		select distinct id, 'movie' from unnest($1::int[]) as id
		on conflict (id, season_number, media_type) do nothing
	`, movieIDs)

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
		order by release_date asc nulls last, position asc nulls last
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
//
// Features come from the generated genre_ids / keyword_ids columns rather
// than the JSONB they are derived from: a candidate pool is 1,200 rows, and
// shipping every keyword name with them was most of the feed's DB time.
type RecoCandidateRow struct {
	MovieID      int           `db:"movie_id"`
	MediaID      int           `db:"media_id"`
	Title        *string       `db:"title"`
	PosterPath   *string       `db:"poster_path"`
	BackdropPath *string       `db:"backdrop_path"`
	GenreIDs     db.IntArray   `db:"genre_ids"`
	KeywordIDs   db.IntArray   `db:"keyword_ids"`
	ReleaseDate  *sql.NullTime `db:"release_date"`
	VoteAverage  float64       `db:"vote_average"`
	VoteCount    int           `db:"vote_count"`
}

// recoCandidateColumns is the select list every RecoCandidateRow query uses,
// over `movies m join media_index mi`.
const recoCandidateColumns = `
	m.movie_id, mi.media_id, m.title, m.poster_path, m.backdrop_path,
	m.genre_ids, m.keyword_ids, m.release_date, m.vote_average, m.vote_count`

// CandidatesByMediaIDs returns the feed projection for a specific set of
// media_index media_ids — used to load features for items in a user's
// interaction history.
func (r *Repository) CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]RecoCandidateRow, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select `+recoCandidateColumns+`
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where mi.media_id = any($1)
	`, mediaIDs)
	return rows, err
}

// CardDetails is the card text the feed shows for one movie: its synopsis,
// its runtime in minutes (0 when unknown), and its genre names in TMDB order.
type CardDetails struct {
	MediaID  int
	Overview string
	Runtime  int
	Genres   []string
}

// CardDetailsByMediaIDs returns the card text for a ranked feed. It is kept
// out of the candidate projection on purpose: the pool is 1,200 rows and only
// the ranked few need a synopsis.
func (r *Repository) CardDetailsByMediaIDs(ctx context.Context, mediaIDs []int) ([]CardDetails, error) {
	if len(mediaIDs) == 0 {
		return []CardDetails{}, nil
	}
	var rows []struct {
		MediaID  int              `db:"media_id"`
		Overview string           `db:"overview"`
		Runtime  int              `db:"runtime"`
		Genres   *json.RawMessage `db:"genres"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select mi.media_id, coalesce(m.overview, '') as overview,
		       coalesce(m.runtime, 0) as runtime, m.genres
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where mi.media_id = any($1)
	`, mediaIDs); err != nil {
		return nil, fmt.Errorf("select card details: %w", err)
	}

	out := make([]CardDetails, len(rows))
	for i, row := range rows {
		out[i] = CardDetails{
			MediaID:  row.MediaID,
			Overview: row.Overview,
			Runtime:  row.Runtime,
			Genres:   genreNames(row.Genres),
		}
	}
	return out, nil
}

// genreNames reads the names out of a TMDB genres array, in order. A missing
// or malformed column yields no names: the card goes without genres rather
// than failing the whole feed over one row.
func genreNames(raw *json.RawMessage) []string {
	names := []string{}
	if raw == nil {
		return names
	}
	var genres []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(*raw, &genres); err != nil {
		return names
	}
	for _, g := range genres {
		if g.Name != "" {
			names = append(names, g.Name)
		}
	}
	return names
}

// LookupGenreNames returns id→name pairs for the requested genre ids from
// the JSONB genres column of the movies cache. Ids that don't appear in any
// cached movie are simply absent from the returned map.
func (r *Repository) LookupGenreNames(ctx context.Context, ids []int) (map[int]string, error) {
	return r.lookupJSONBNames(ctx, "genres", "genre_ids", ids)
}

// LookupKeywordNames is the keywords twin of LookupGenreNames.
func (r *Repository) LookupKeywordNames(ctx context.Context, ids []int) (map[int]string, error) {
	return r.lookupJSONBNames(ctx, "keywords", "keyword_ids", ids)
}

// lookupJSONBNames reads names out of `column` (a JSONB array of {id, name}
// objects). The GIN-indexed `idColumn` narrows the scan to movies that carry
// one of the ids, instead of unpacking every movie's array. Both columns are
// fixed internal identifiers, never user input.
func (r *Repository) lookupJSONBNames(ctx context.Context, column, idColumn string, ids []int) (map[int]string, error) {
	if len(ids) == 0 {
		return map[int]string{}, nil
	}
	query := `
		select distinct (elem->>'id')::int as id, elem->>'name' as name
		from movies, jsonb_array_elements(` + column + `) elem
		where ` + idColumn + ` && $1::int[] and (elem->>'id')::int = any($1)
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

// RecoCandidatesChangedSince returns the feed projection of every indexed
// movie written at or after since, voted or not. The zero time reads the
// whole catalog. It fills the in-process catalog the feed draws from.
func (r *Repository) RecoCandidatesChangedSince(ctx context.Context, since time.Time) ([]RecoCandidateRow, error) {
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select `+recoCandidateColumns+`
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where coalesce(m.updated_at, 'epoch') >= $1
	`, since)
	return rows, err
}

// ListCandidatesForReco returns up to `limit` movies suitable as feed
// candidates, joined with their media_index id. Cheap query — feed
// generation is sub-second.
func (r *Repository) ListCandidatesForReco(ctx context.Context, limit int) ([]RecoCandidateRow, error) {
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select `+recoCandidateColumns+`
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where m.vote_count > 0
		order by m.vote_count desc, mi.media_id
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
		select `+recoCandidateColumns+`
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where m.vote_count >= $1 and m.`+column+` && $2::int[]
		order by m.vote_count desc, mi.media_id
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
			position,
			release_date
		)
		values (
			:collection_id,
			:movie_id,
			:name,
			:poster_path,
			:vote_average,
			:position,
			:release_date
		)
		on conflict do nothing
	`, entries)

	return err
}

// ListStaleCollections returns up to limit collections whose oldest part was
// written before cutoff, oldest first.
func (r *Repository) ListStaleCollections(ctx context.Context, cutoff time.Time, limit int) ([]StaleRow, error) {
	rows := []StaleRow{}
	err := r.db.SelectContext(ctx, &rows, `
		select collection_id as id, min(coalesce(created_at, 'epoch')) as updated_at
		from movie_collections
		group by collection_id
		having min(coalesce(created_at, 'epoch')) < $1
		order by 2, 1
		limit $2
	`, cutoff, limit)
	return rows, err
}

// ReplaceCollection swaps a collection's cached parts for fresh ones in one
// transaction, so parts TMDB no longer lists go and every part is stamped now.
func (r *Repository) ReplaceCollection(ctx context.Context, collectionID int, parts []MovieCollection) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `delete from movie_collections where collection_id = $1`, collectionID); err != nil {
		return fmt.Errorf("clear collection %d: %w", collectionID, err)
	}
	if len(parts) > 0 {
		if _, err := tx.NamedExecContext(ctx, `
			insert into movie_collections (collection_id, movie_id, name, poster_path, vote_average, position, release_date)
			values (:collection_id, :movie_id, :name, :poster_path, :vote_average, :position, :release_date)
			on conflict do nothing
		`, parts); err != nil {
			return fmt.Errorf("write collection %d: %w", collectionID, err)
		}
	}
	return tx.Commit()
}

// DeleteCollection drops a collection from the cache.
func (r *Repository) DeleteCollection(ctx context.Context, collectionID int) error {
	_, err := r.db.ExecContext(ctx, `delete from movie_collections where collection_id = $1`, collectionID)
	return err
}

// RecoCrew returns the TMDB person ids of each cached film's crew in the given
// jobs, keyed by movie id, sorted and without repeats. Films with no such crew
// are left out. The recommender's crew term matches titles on them.
func (r *Repository) RecoCrew(ctx context.Context, jobs []string) (map[int][]int, error) {
	var rows []struct {
		ID  int         `db:"id"`
		IDs db.IntArray `db:"ids"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select id, ids from (
			select movie_id as id,
			       array(select distinct (c->>'id')::int
			               from jsonb_array_elements(case when jsonb_typeof(credits->'crew') = 'array'
			                                              then credits->'crew' else '[]'::jsonb end) c
			              where c->>'job' = any($1) and jsonb_typeof(c->'id') = 'number'
			              order by 1) as ids
			  from movies
			 where credits is not null
		) t
		where cardinality(ids) > 0`, jobs); err != nil {
		return nil, fmt.Errorf("reco crew: %w", err)
	}
	out := make(map[int][]int, len(rows))
	for _, row := range rows {
		out[row.ID] = row.IDs
	}
	return out, nil
}

// RecoKeywordSets returns the keyword ids of every cached film with a vote and
// at least two keywords, one slice per film. The related-keyword term counts
// which keywords tag the same films.
func (r *Repository) RecoKeywordSets(ctx context.Context) ([][]int, error) {
	var rows []db.IntArray
	if err := r.db.SelectContext(ctx, &rows, `
		select keyword_ids from movies
		where vote_count > 0 and cardinality(keyword_ids) > 1`); err != nil {
		return nil, fmt.Errorf("reco keyword sets: %w", err)
	}
	out := make([][]int, len(rows))
	for i, row := range rows {
		out[i] = row
	}
	return out, nil
}

// RecoCast returns the TMDB person ids of each cached film's first top billed
// cast, keyed by movie id, sorted and without repeats. Films with no cached
// cast are left out. The recommender's random walk steps through them.
func (r *Repository) RecoCast(ctx context.Context, top int) (map[int][]int, error) {
	var rows []struct {
		ID  int         `db:"id"`
		IDs db.IntArray `db:"ids"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select id, ids from (
			select movie_id as id,
			       array(select distinct (c->>'id')::int
			               from jsonb_array_elements(case when jsonb_typeof(credits->'cast') = 'array'
			                                              then credits->'cast' else '[]'::jsonb end) c
			              where jsonb_typeof(c->'id') = 'number' and jsonb_typeof(c->'order') = 'number'
			                and (c->>'order')::int < $1
			              order by 1) as ids
			  from movies
			 where credits is not null
		) t
		where cardinality(ids) > 0`, top); err != nil {
		return nil, fmt.Errorf("reco cast: %w", err)
	}
	out := make(map[int][]int, len(rows))
	for _, row := range rows {
		out[row.ID] = row.IDs
	}
	return out, nil
}
