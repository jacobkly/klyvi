package onboarding

import "time"

// PoolEntry mirrors a row in `onboarding_pool`. The pool is the curated
// starter set the frontend shows during the cold-start swipe deck — see
// `docs/onboarding-spec.md` §4 for the curation criteria.
type PoolEntry struct {
	TMDBID       int64     `db:"tmdb_id"       json:"tmdb_id"`
	Dimension    string    `db:"dimension"     json:"dimension"`
	DisplayOrder int       `db:"display_order" json:"display_order"`
	Active       bool      `db:"active"        json:"active"`
	AddedAt      time.Time `db:"added_at"      json:"added_at"`
}

// PoolRow is an active pool entry joined with its cached `movies` row. The
// catalog fields are nil when the film is not cached. Detailed is true once
// the cached row has credits, which is what the movies service treats as a
// full detail fetch rather than a search hit.
type PoolRow struct {
	TMDBID      int64      `db:"tmdb_id"`
	Dimension   string     `db:"dimension"`
	Title       *string    `db:"title"`
	PosterPath  *string    `db:"poster_path"`
	ReleaseDate *time.Time `db:"release_date"`
	Detailed    bool       `db:"detailed"`
}

// EnrichedPoolEntry is what `GET /v1/onboarding/pool` returns — a pool row
// joined with the cached `movies` row so the frontend can render posters
// without per-item lookups.
type EnrichedPoolEntry struct {
	TMDBID      int64  `json:"tmdb_id"`
	Title       string `json:"title"`
	PosterPath  string `json:"poster_path"`
	ReleaseYear int    `json:"release_year"`
	Dimension   string `json:"dimension"`
}
