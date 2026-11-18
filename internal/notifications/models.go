// Package notifications holds the in-app notices: a season a user tracks
// premiering, a film on their planning list coming out, or a change to the
// terms or privacy policy. The nightly job in cmd/notify writes the title
// notices, cmd/notify -policy posts a policy one, and the API lists them and
// marks them read.
package notifications

import "time"

// The kinds of notice. Each one fixes its media type, and the table's
// notifications_kind_shape constraint holds them to it. A policy notice has no
// media type or title id, only its text in Title and a site path in Link.
const (
	KindSeasonPremiere = "season_premiere"
	KindFilmRelease    = "film_release"
	KindPolicy         = "policy"
)

// MaxPolicyText caps a policy notice's text, which the notice list shows whole.
const MaxPolicyText = 200

// Page sizes for the list endpoint. An oversized request is cut down to
// MaxPageSize rather than refused.
const (
	DefaultPageSize = 30
	MaxPageSize     = 100
)

// Notification is one notice as the client sees it. PosterPath is joined from
// the catalog when the notice is read, so it is nil for a title no longer in
// the cache. EventDate is a plain YYYY-MM-DD with no time zone to shift it. A
// policy notice has nil MediaType, TMDBID and PosterPath, and a Link.
type Notification struct {
	ID           int64      `db:"id" json:"id"`
	Kind         string     `db:"kind" json:"kind"`
	MediaType    *string    `db:"media_type" json:"media_type"`
	TMDBID       *int       `db:"tmdb_id" json:"tmdb_id"`
	SeasonNumber *int       `db:"season_number" json:"season_number"`
	Title        string     `db:"title" json:"title"`
	PosterPath   *string    `db:"poster_path" json:"poster_path"`
	EventDate    string     `db:"event_date" json:"event_date"`
	Link         *string    `db:"link" json:"link"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	ReadAt       *time.Time `db:"read_at" json:"read_at"`
}

// Page is one page of a user's notices. UnreadCount covers all of them, not
// just this page, because it drives the dot on the user menu.
type Page struct {
	Notifications []Notification `json:"notifications"`
	UnreadCount   int            `json:"unread_count"`
	HasMore       bool           `json:"has_more"`
}
