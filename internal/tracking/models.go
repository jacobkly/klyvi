package tracking

import (
	"time"

	"github.com/google/uuid"
)

// Status values constrained by the media_list CHECK constraint. Mirrored
// here so the service can validate before hitting the DB and produce a
// clearer 400 than a Postgres constraint violation.
const (
	StatusWatching   = "watching"
	StatusPlanning   = "planning"
	StatusCompleted  = "completed"
	StatusRewatching = "rewatching"
	StatusPaused     = "paused"
	StatusDropped    = "dropped"
)

// MediaType values constrained by the media_list and media_index CHECK
// constraints. The committed convention is ('movie','season'), not 'tv'.
const (
	MediaTypeMovie  = "movie"
	MediaTypeSeason = "season"
)

// Entry mirrors a row in the media_list table — one tracked item for one
// user — enriched with display fields joined from the catalog so the
// frontend can render a row without follow-up lookups. The display
// fields are nullable: a movie row has movie fields set and season
// fields nil; a season row has the parent series' TMDBID + Title and
// the season's PosterPath/SeasonNumber/SeasonName set.
//
// UNIQUE(user_id, media_id) is enforced in-schema.
type Entry struct {
	ID              int        `db:"id" json:"id"`
	UserID          uuid.UUID  `db:"user_id" json:"user_id"`
	MediaID         int        `db:"media_id" json:"media_id"`
	MediaType       string     `db:"media_type" json:"media_type"`
	Status          *string    `db:"status" json:"status"`
	Score           *int       `db:"score" json:"score"`
	EpisodeProgress *int       `db:"episode_progress" json:"episode_progress"`
	StartDate       *time.Time `db:"start_date" json:"start_date"`
	FinishDate      *time.Time `db:"finish_date" json:"finish_date"`
	TotalRewatches  int        `db:"total_rewatches" json:"total_rewatches"`
	Notes           *string    `db:"notes" json:"notes"`
	IsDeleted       bool       `db:"is_deleted" json:"is_deleted"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`

	// Display fields, populated by the JOIN against movies / tv_series /
	// tv_seasons. Nullable when the catalog row is not yet cached or the
	// field doesn't apply to the media_type.
	TMDBID       *int    `db:"tmdb_id" json:"tmdb_id"`
	Title        *string `db:"title" json:"title"`
	PosterPath   *string `db:"poster_path" json:"poster_path"`
	BackdropPath *string `db:"backdrop_path" json:"backdrop_path"`
	ReleaseYear  *int    `db:"release_year" json:"release_year"`
	SeasonNumber *int    `db:"season_number" json:"season_number"`
	SeasonName   *string `db:"season_name" json:"season_name"`
}
