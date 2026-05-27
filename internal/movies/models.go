package movies

import (
	"encoding/json"
	"time"
)

type Movie struct {
	MovieID             int              `db:"movie_id" json:"movie_id"`
	Adult               bool             `db:"adult" json:"adult"`
	BackdropPath        *string          `db:"backdrop_path" json:"backdrop_path"`
	BelongsToCollection *json.RawMessage `db:"belongs_to_collection" json:"belongs_to_collection"`
	Budget              int              `db:"budget" json:"budget"`
	Genres              *json.RawMessage `db:"genres" json:"genres"`
	Homepage            *string          `db:"homepage" json:"homepage"`
	IMDBID              *string          `db:"imdb_id" json:"imdb_id"`
	OriginalLanguage    *string          `db:"original_language" json:"original_language"`
	OriginalTitle       *string          `db:"original_title" json:"original_title"`
	Overview            *string          `db:"overview" json:"overview"`
	Popularity          float64          `db:"popularity" json:"popularity"`
	PosterPath          *string          `db:"poster_path" json:"poster_path"`
	ProductionCompanies *json.RawMessage `db:"production_companies" json:"production_companies"`
	ProductionCountries *json.RawMessage `db:"production_countries" json:"production_countries"`
	ReleaseDate         *time.Time       `db:"release_date" json:"release_date"`
	Revenue             int64            `db:"revenue" json:"revenue"`
	Runtime             int              `db:"runtime" json:"runtime"`
	SpokenLanguages     *json.RawMessage `db:"spoken_languages" json:"spoken_languages"`
	Status              *string          `db:"status" json:"status"`
	Tagline             *string          `db:"tagline" json:"tagline"`
	Title               *string          `db:"title" json:"title"`
	Video               bool             `db:"video" json:"video"`
	VoteAverage         float64          `db:"vote_average" json:"vote_average"`
	VoteCount           int              `db:"vote_count" json:"vote_count"`
	Keywords            *json.RawMessage `db:"keywords" json:"keywords"`
	Credits             *json.RawMessage `db:"credits" json:"credits"`
	CreatedAt           time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time        `db:"updated_at" json:"updated_at"`
}

type MovieCollection struct {
	CollectionID int       `db:"collection_id" json:"collection_id"`
	MovieID      int       `db:"movie_id" json:"movie_id"`
	Name         string    `db:"name" json:"name"`
	PosterPath   *string   `db:"poster_path" json:"poster_path"`
	VoteAverage  *float64  `db:"vote_average" json:"vote_average"`
	Position     *int      `db:"position" json:"position"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}
