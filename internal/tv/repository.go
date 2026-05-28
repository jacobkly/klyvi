package tv

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

func (r *Repository) InsertTVSeries(ctx context.Context, series *TVSeries) error {
	_, err := r.db.NamedExecContext(ctx, `
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
		on conflict (tv_id) do nothing
	`, series)

	return err
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
