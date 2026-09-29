package tv

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/platform/tmdb"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// tvSeriesColumns is the explicit column list used by SELECT and INSERT against
// tv_series. The struct deliberately omits the array columns
// (episode_run_time, languages, origin_country) so they are absent here too.
const tvSeriesColumns = `tv_id, adult, backdrop_path, created_by, first_air_date,
	genres, homepage, in_production, last_air_date, last_episode_to_air,
	next_episode_to_air, networks, number_of_episodes, number_of_seasons,
	name, original_language, original_name, overview, popularity, poster_path,
	production_companies, production_countries, seasons, spoken_languages,
	status, tagline, type, vote_average, vote_count, keywords, credits,
	created_at, updated_at`

func (r *Repository) GetTVSeriesByID(ctx context.Context, tvID int) (*TVSeries, error) {
	var series TVSeries
	err := r.db.GetContext(ctx, &series,
		`select `+tvSeriesColumns+` from tv_series where tv_id = $1`, tvID)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &series, err
}

// upsertTVSeriesSQL writes every column of a series and stamps updated_at
// when the row already exists. InsertTVSeries narrows when it may overwrite.
const upsertTVSeriesSQL = `
		insert into tv_series (
			tv_id, adult, backdrop_path, created_by, first_air_date,
			genres, homepage, in_production, last_air_date, last_episode_to_air,
			next_episode_to_air, networks, number_of_episodes, number_of_seasons,
			name, original_language, original_name, overview, popularity, poster_path,
			production_companies, production_countries, seasons, spoken_languages,
			status, tagline, type, vote_average, vote_count, keywords, credits
		) values (
			:tv_id, :adult, :backdrop_path, :created_by, :first_air_date,
			:genres, :homepage, :in_production, :last_air_date, :last_episode_to_air,
			:next_episode_to_air, :networks, :number_of_episodes, :number_of_seasons,
			:name, :original_language, :original_name, :overview, :popularity, :poster_path,
			:production_companies, :production_countries, :seasons, :spoken_languages,
			:status, :tagline, :type, :vote_average, :vote_count, :keywords, :credits
		)
		on conflict (tv_id) do update set
			adult = excluded.adult,
			backdrop_path = excluded.backdrop_path,
			created_by = excluded.created_by,
			first_air_date = excluded.first_air_date,
			genres = excluded.genres,
			homepage = excluded.homepage,
			in_production = excluded.in_production,
			last_air_date = excluded.last_air_date,
			last_episode_to_air = excluded.last_episode_to_air,
			next_episode_to_air = excluded.next_episode_to_air,
			networks = excluded.networks,
			number_of_episodes = excluded.number_of_episodes,
			number_of_seasons = excluded.number_of_seasons,
			name = excluded.name,
			original_language = excluded.original_language,
			original_name = excluded.original_name,
			overview = excluded.overview,
			popularity = excluded.popularity,
			poster_path = excluded.poster_path,
			production_companies = excluded.production_companies,
			production_countries = excluded.production_countries,
			seasons = excluded.seasons,
			spoken_languages = excluded.spoken_languages,
			status = excluded.status,
			tagline = excluded.tagline,
			type = excluded.type,
			vote_average = excluded.vote_average,
			vote_count = excluded.vote_count,
			keywords = excluded.keywords,
			credits = excluded.credits,
			updated_at = now()`

// InsertTVSeries caches a series. A row cached without keywords or credits
// is replaced by a full detail record, so a series always ends up with the
// features the recommender reads. A full row is never replaced by a partial
// one.
func (r *Repository) InsertTVSeries(ctx context.Context, series *TVSeries) error {
	_, err := r.db.NamedExecContext(ctx, upsertTVSeriesSQL+`
		where (tv_series.keywords is null or tv_series.credits is null)
		  and excluded.keywords is not null and excluded.credits is not null
	`, series)

	return err
}

// ReplaceTVSeries overwrites a cached series with a fresh TMDB record,
// whatever the cached row holds, and marks it fresh.
func (r *Repository) ReplaceTVSeries(ctx context.Context, series *TVSeries) error {
	_, err := r.db.NamedExecContext(ctx, upsertTVSeriesSQL, series)
	return err
}

// DeleteSeries drops a series and its seasons from the cache. Their
// media_index rows stay, and with them anything users tracked or rated.
func (r *Repository) DeleteSeries(ctx context.Context, tvID int) error {
	_, err := r.db.ExecContext(ctx, `
		with seasons as (delete from tv_seasons where tv_id = $1)
		delete from tv_series where tv_id = $1
	`, tvID)
	return err
}

// StaleSeries is a cached series due for a refresh. A row never stamped
// counts as written at the epoch.
type StaleSeries struct {
	TVID      int       `db:"tv_id"`
	UpdatedAt time.Time `db:"updated_at"`
}

// ListStaleSeries returns up to limit series last written before cutoff,
// oldest first.
func (r *Repository) ListStaleSeries(ctx context.Context, cutoff time.Time, limit int) ([]StaleSeries, error) {
	rows := []StaleSeries{}
	err := r.db.SelectContext(ctx, &rows, `
		select tv_id, coalesce(updated_at, 'epoch') as updated_at
		from tv_series
		where coalesce(updated_at, 'epoch') < $1
		order by 2, 1
		limit $2
	`, cutoff, limit)
	return rows, err
}

// HasTVSeriesFeatures reports whether a series is cached with its keywords
// and credits.
func (r *Repository) HasTVSeriesFeatures(ctx context.Context, tvID int) (bool, error) {
	var ok bool
	err := r.db.GetContext(ctx, &ok, `
		select exists (
			select 1 from tv_series
			where tv_id = $1 and keywords is not null and credits is not null
		)
	`, tvID)
	return ok, err
}

// EnsureMediaIndexSeries writes the series' own media_index row: type 'tv',
// keyed by tv_id with no season number.
func (r *Repository) EnsureMediaIndexSeries(ctx context.Context, tvID int) error {
	_, err := r.db.ExecContext(ctx, `
		insert into media_index (id, media_type)
		values ($1, 'tv')
		on conflict (id, season_number, media_type) do nothing
	`, tvID)

	return err
}

// RefreshFeatureStats recomputes the 'tv' partition of the recommender's
// inverse-document-frequency inputs: over voted series (vote_count > 0), how
// many carry each genre and keyword id, read from the generated arrays
// (00030), plus the corpus size. One transaction, so a reader never sees a
// half-rebuilt table. The movie rows are left alone.
func (r *Repository) RefreshFeatureStats(ctx context.Context) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`delete from feature_stats where media_type = 'tv'`); err != nil {
		return fmt.Errorf("clear feature_stats: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		insert into feature_stats (media_type, feature_type, feature_id, doc_count)
		select 'tv', 'genre', g, count(*)
		from tv_series, unnest(genre_ids) g
		where vote_count > 0
		group by g
	`); err != nil {
		return fmt.Errorf("count genres: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		insert into feature_stats (media_type, feature_type, feature_id, doc_count)
		select 'tv', 'keyword', k, count(*)
		from tv_series, unnest(keyword_ids) k
		where vote_count > 0
		group by k
	`); err != nil {
		return fmt.Errorf("count keywords: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		insert into feature_corpus (media_type, doc_count, updated_at)
		values ('tv', (select count(*) from tv_series where vote_count > 0), now())
		on conflict (media_type) do update
			set doc_count = excluded.doc_count, updated_at = now()
	`); err != nil {
		return fmt.Errorf("count corpus: %w", err)
	}

	return tx.Commit()
}

// SlimStoredCredits trims the stored credits of the given series with
// tmdb.SlimCredits, for rows cached before credits were trimmed on write. It
// rewrites only the rows that lose entries and returns how many that was.
func (r *Repository) SlimStoredCredits(ctx context.Context, tvIDs []int) (int, error) {
	var rows []struct {
		ID      int    `db:"tv_id"`
		Credits []byte `db:"credits"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select tv_id, credits from tv_series
		where tv_id = any($1) and credits is not null
	`, tvIDs); err != nil {
		return 0, fmt.Errorf("read credits: %w", err)
	}

	ids := make([]int, 0, len(rows))
	slimmed := make([]string, 0, len(rows))
	for _, row := range rows {
		out, changed, err := tmdb.SlimCreditsJSON(row.Credits)
		if err != nil {
			return 0, fmt.Errorf("series %d: %w", row.ID, err)
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
		update tv_series s set credits = v.credits::jsonb
		from unnest($1::int[], $2::text[]) as v(id, credits)
		where s.tv_id = v.id
	`, ids, slimmed); err != nil {
		return 0, fmt.Errorf("write credits: %w", err)
	}
	return len(ids), nil
}

func (r *Repository) GetTVSeasonByTVIDAndNumber(ctx context.Context, tvID, seasonNumber int) (*TVSeason, error) {
	var season TVSeason
	err := r.db.GetContext(ctx, &season,
		`select * from tv_seasons where tv_id = $1 and season_number = $2`,
		tvID, seasonNumber)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &season, err
}

func (r *Repository) InsertTVSeason(ctx context.Context, season *TVSeason) error {
	_, err := r.db.NamedExecContext(ctx, `
		insert into tv_seasons (
			tv_id, season_number, air_date, name, overview,
			poster_path, vote_average, vote_count, episodes, networks
		) values (
			:tv_id, :season_number, :air_date, :name, :overview,
			:poster_path, :vote_average, :vote_count, :episodes, :networks
		)
		on conflict (tv_id, season_number) do nothing
	`, season)

	return err
}

// ReplaceTVSeason overwrites a cached season with a fresh TMDB record and
// marks it fresh.
func (r *Repository) ReplaceTVSeason(ctx context.Context, season *TVSeason) error {
	_, err := r.db.NamedExecContext(ctx, `
		insert into tv_seasons (
			tv_id, season_number, air_date, name, overview,
			poster_path, vote_average, vote_count, episodes, networks
		) values (
			:tv_id, :season_number, :air_date, :name, :overview,
			:poster_path, :vote_average, :vote_count, :episodes, :networks
		)
		on conflict (tv_id, season_number) do update set
			air_date = excluded.air_date,
			name = excluded.name,
			overview = excluded.overview,
			poster_path = excluded.poster_path,
			vote_average = excluded.vote_average,
			vote_count = excluded.vote_count,
			episodes = excluded.episodes,
			networks = excluded.networks,
			updated_at = now()
	`, season)

	return err
}

// DeleteSeason drops one season from the cache. Its media_index row stays.
func (r *Repository) DeleteSeason(ctx context.Context, tvID, seasonNumber int) error {
	_, err := r.db.ExecContext(ctx,
		`delete from tv_seasons where tv_id = $1 and season_number = $2`, tvID, seasonNumber)
	return err
}

// StaleSeason is a cached season due for a refresh.
type StaleSeason struct {
	TVID         int       `db:"tv_id"`
	SeasonNumber int       `db:"season_number"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// ListStaleSeasons returns up to limit seasons last written before cutoff,
// oldest first.
func (r *Repository) ListStaleSeasons(ctx context.Context, cutoff time.Time, limit int) ([]StaleSeason, error) {
	rows := []StaleSeason{}
	err := r.db.SelectContext(ctx, &rows, `
		select tv_id, season_number, coalesce(updated_at, 'epoch') as updated_at
		from tv_seasons
		where coalesce(updated_at, 'epoch') < $1
		order by 3, 1, 2
		limit $2
	`, cutoff, limit)
	return rows, err
}

// EnsureMediaIndexSeason writes a media_index row for the (tv, season) pair.
// season_number is non-null here, so the slice-2 1A NULLS NOT DISTINCT fix is
// strictly speaking not needed for this path — ON CONFLICT works on normal
// non-null tuples. The clause is included anyway for idempotent retries.
func (r *Repository) EnsureMediaIndexSeason(ctx context.Context, tvID, seasonNumber int) error {
	_, err := r.db.ExecContext(ctx, `
		insert into media_index (id, season_number, media_type)
		values ($1, $2, 'season')
		on conflict (id, season_number, media_type) do nothing
	`, tvID, seasonNumber)

	return err
}

// GetTVIDAndSeasonByMediaID resolves an internal media_index media_id back to
// the (tv_id, season_number) pair so the service can dispatch to TMDB or the
// season cache. Returns (0, 0, nil) when not found.
func (r *Repository) GetTVIDAndSeasonByMediaID(ctx context.Context, mediaID int) (int, int, error) {
	var row struct {
		ID           int `db:"id"`
		SeasonNumber int `db:"season_number"`
	}
	err := r.db.GetContext(ctx, &row, `
		select id, season_number
		from media_index
		where media_id = $1 and media_type = 'season'
	`, mediaID)

	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return row.ID, row.SeasonNumber, nil
}
