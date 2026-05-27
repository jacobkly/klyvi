package tv

import (
	"encoding/json"
	"time"
)

// TVSeries mirrors the columns of the `tv_series` table that the cache + the
// (future) recommender actually use. Postgres array columns on the table
// (`episode_run_time`, `languages`, `origin_country`) are intentionally NOT
// modeled here — pgx-stdlib does not round-trip native Go slices without
// extra codec setup, and the recommender does not consume those fields. The
// columns stay NULL on rows we write and are ignored on read; if any feature
// ever needs them, add the field and use a typed wrapper at that time.
type TVSeries struct {
	TVID                int              `db:"tv_id" json:"tv_id"`
	Adult               bool             `db:"adult" json:"adult"`
	BackdropPath        *string          `db:"backdrop_path" json:"backdrop_path"`
	CreatedBy           *json.RawMessage `db:"created_by" json:"created_by"`
	FirstAirDate        *time.Time       `db:"first_air_date" json:"first_air_date"`
	Genres              *json.RawMessage `db:"genres" json:"genres"`
	Homepage            *string          `db:"homepage" json:"homepage"`
	InProduction        bool             `db:"in_production" json:"in_production"`
	LastAirDate         *time.Time       `db:"last_air_date" json:"last_air_date"`
	LastEpisodeToAir    *json.RawMessage `db:"last_episode_to_air" json:"last_episode_to_air"`
	NextEpisodeToAir    *json.RawMessage `db:"next_episode_to_air" json:"next_episode_to_air"`
	Networks            *json.RawMessage `db:"networks" json:"networks"`
	NumberOfEpisodes    int              `db:"number_of_episodes" json:"number_of_episodes"`
	NumberOfSeasons     int              `db:"number_of_seasons" json:"number_of_seasons"`
	OriginalLanguage    *string          `db:"original_language" json:"original_language"`
	OriginalName        *string          `db:"original_name" json:"original_name"`
	Overview            *string          `db:"overview" json:"overview"`
	Popularity          float64          `db:"popularity" json:"popularity"`
	PosterPath          *string          `db:"poster_path" json:"poster_path"`
	ProductionCompanies *json.RawMessage `db:"production_companies" json:"production_companies"`
	ProductionCountries *json.RawMessage `db:"production_countries" json:"production_countries"`
	Seasons             *json.RawMessage `db:"seasons" json:"seasons"`
	SpokenLanguages     *json.RawMessage `db:"spoken_languages" json:"spoken_languages"`
	Status              *string          `db:"status" json:"status"`
	Tagline             *string          `db:"tagline" json:"tagline"`
	Type                *string          `db:"type" json:"type"`
	VoteAverage         float64          `db:"vote_average" json:"vote_average"`
	VoteCount           int              `db:"vote_count" json:"vote_count"`
	Keywords            *json.RawMessage `db:"keywords" json:"keywords"`
	Credits             *json.RawMessage `db:"credits" json:"credits"`
	CreatedAt           time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time        `db:"updated_at" json:"updated_at"`
}

// TVSeason mirrors `tv_seasons`. Composite PK is (tv_id, season_number).
type TVSeason struct {
	TVID         int              `db:"tv_id" json:"tv_id"`
	SeasonNumber int              `db:"season_number" json:"season_number"`
	AirDate      *time.Time       `db:"air_date" json:"air_date"`
	Name         *string          `db:"name" json:"name"`
	Overview     *string          `db:"overview" json:"overview"`
	PosterPath   *string          `db:"poster_path" json:"poster_path"`
	VoteAverage  float64          `db:"vote_average" json:"vote_average"`
	VoteCount    int              `db:"vote_count" json:"vote_count"`
	Episodes     *json.RawMessage `db:"episodes" json:"episodes"`
	Networks     *json.RawMessage `db:"networks" json:"networks"`
	CreatedAt    time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time        `db:"updated_at" json:"updated_at"`
}
