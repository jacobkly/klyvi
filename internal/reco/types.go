package reco

import (
	"slices"

	"github.com/google/uuid"
)

// MediaFeatures is the feature view of a media item the recommender uses
// at scoring time. Built from the `movies` cache (no precomputed feature
// table yet — future work). All TV-side fields are stubbed because the
// movies recommender is built first; TV reco is later work.
type MediaFeatures struct {
	MediaID     int     `json:"media_id"`
	MediaType   string  `json:"media_type"`
	GenreIDs    []int   `json:"genre_ids"`
	KeywordIDs  []int   `json:"keyword_ids"`
	Year        int     `json:"year"` // 0 if unknown
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
}

// Candidate is a media item the orchestrator may rank for a user. Features
// can be nil for Tier 0 (cold start) — Tier 1+ require populated features.
//
// The display fields (TMDBID, Title, PosterPath, BackdropPath, ReleaseYear,
// VoteAverage) are populated by the catalog adapter so the feed handler can
// return everything the frontend needs to render a card without an N+1
// follow-up lookup. The scorers themselves do not read these fields — they
// ride through the embedding into Scored unchanged.
//
// Overview, Runtime, SeasonCount and Genres are filled only on the ranked
// feed, after scoring (see CardDetails), so the candidate pool never carries
// them. A series has a season count where a film has a runtime.
type Candidate struct {
	MediaID      int            `json:"media_id"`
	MediaType    string         `json:"media_type"`
	TMDBID       int            `json:"tmdb_id"`
	Title        string         `json:"title"`
	PosterPath   string         `json:"poster_path"`
	BackdropPath string         `json:"backdrop_path"`
	ReleaseYear  int            `json:"release_year"`
	VoteAverage  float64        `json:"vote_average"`
	Overview     string         `json:"overview"`
	Runtime      int            `json:"runtime"`
	SeasonCount  int            `json:"season_count"`
	Genres       []string       `json:"genres"`
	Features     *MediaFeatures `json:"features"`
}

// CardDetails is the text a feed card shows beyond its artwork and title:
// the synopsis, the genre names in TMDB's order, and the runtime in minutes
// for a film or the season count for a series (0 when unknown, and 0 for the
// field the other type has).
type CardDetails struct {
	Overview    string
	Runtime     int
	Genres      []string
	SeasonCount int
}

// Scored is a ranked Candidate plus the score and structured reasons
// (used for paid-tier explanations and for harness/debug output).
type Scored struct {
	Candidate
	Score   float64  `json:"score"`
	Reasons []Reason `json:"reasons"`
}

// Reason is a single explainability token attached to a Scored item.
// Kind "because" names a title the user rated highly that the pick shares
// keywords with: ID is that title's TMDB id and Name its title, always set.
// Otherwise Kind is "keyword" or "genre"; ID is the TMDB feature id; Name is the
// human-readable label resolved server-side from the movies cache (empty
// when the catalog doesn't carry the name for that id — frontend can
// render the id as a fallback or hide it).
type Reason struct {
	Kind string `json:"kind"`
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
}

// SignalItem is one piece of user signal — a media item with the weighted
// strength of the user's preference. Positive = liked, negative = disliked.
type SignalItem struct {
	MediaID  int
	Weight   float64
	Features *MediaFeatures
}

// UserContext bundles everything a Scorer needs to know about the user.
// SeenMediaIDs is the union of interaction-touched and watchlist-tracked
// items — candidates with these ids are filtered out before scoring.
type UserContext struct {
	UserID       uuid.UUID
	Liked        []SignalItem
	Disliked     []SignalItem
	SeenMediaIDs map[int]bool
}

// Config holds the tunable constants used by the signal pipeline and
// scorers. All defaults can be overridden by the orchestrator's caller.
type Config struct {
	// Base weights per interaction kind (signed; multiplied by rating_adjust + recency_decay).
	BaseLogged     float64
	BaseRated      float64
	BaseDismissed  float64
	BaseSaved      float64
	BaseClicked    float64
	BaseImpression float64

	// Recency decay: weight *= exp(-age_days / HalfLifeDays * ln(2)).
	HalfLifeDays float64

	// Tier-1 / Tier-2 scoring knobs.
	NegativeSimWeight float64 // α in: score = sim(pos) − α * sim(neg)
	QualityFloorVotes int     // candidates with vote_count below this are dropped from Tier 1
	GenreWeight       float64
	KeywordWeight     float64
	EraWeight         float64

	// MMR diversification.
	MMRLambda float64

	// Result size.
	FeedSize int

	// TVShare is the share of the feed kept for series, 0 to 1. It sizes the
	// series draw in the candidate pool, since series vote counts run lower
	// than films' and a pool drawn by votes alone would hold almost no shows.
	// Films fill whatever share the series catalog cannot. It also bounds the
	// ranked feed, so shows cannot take over a film lover's feed (see
	// FeedTypeBound). 0 is the films-only feed and 1 the series-only feed.
	TVShare float64

	// CalibrationWeight holds the feed's genre mix near the mix of the user's
	// likes. MMR pays this much, times the pool's top score, per nat of KL
	// divergence between the two (see Calibration). 0 turns it off.
	CalibrationWeight float64
}

// DefaultConfig is the starting point. All values are tunable via
// orchestrator construction.
func DefaultConfig() Config {
	return Config{
		BaseLogged:     1.0,
		BaseRated:      1.0, // scaled by rating_adjust
		BaseDismissed:  -2.0,
		BaseSaved:      1.2,
		BaseClicked:    0.3,
		BaseImpression: -0.05,
		HalfLifeDays:   120,

		// Weights tuned by the leave-one-out harness over coherent tastes
		// (docs/plans/reco-quality.md): keyword-weighting beats genre-only
		// decisively, and a moderate keyword lead (0.9 vs 0.2) maximises
		// recall without starving genre breadth. Overridable via RECO_* env.
		NegativeSimWeight: 0.6,
		QualityFloorVotes: 25,
		GenreWeight:       0.2,
		KeywordWeight:     0.9,
		EraWeight:         0.1,

		MMRLambda: 0.7,
		FeedSize:  30,
		TVShare:   0.4,
	}
}

// FeedMedia is which media a feed holds: films and series mixed, or one of
// them alone.
type FeedMedia string

const (
	FeedAll    FeedMedia = "all"
	FeedMovies FeedMedia = "movie"
	FeedTV     FeedMedia = "tv"
)

// feedMedia is every FeedMedia, so the cache can drop each variant of a feed.
var feedMedia = []FeedMedia{FeedAll, FeedMovies, FeedTV}

// ParseFeedMedia reads a feed's ?media= value. Empty is the mixed feed, and
// ok is false for anything else the feed does not know.
func ParseFeedMedia(s string) (media FeedMedia, ok bool) {
	if s == "" {
		return FeedAll, true
	}
	if m := FeedMedia(s); slices.Contains(feedMedia, m) {
		return m, true
	}
	return "", false
}

// ForMedia is the config that ranks a feed of the given media. A films-only
// feed runs at TV share 0 and a series-only feed at 1, so the pool draws only
// that type. The mixed feed keeps the configured share.
func (c Config) ForMedia(m FeedMedia) Config {
	switch m {
	case FeedMovies:
		c.TVShare = 0
	case FeedTV:
		c.TVShare = 1
	}
	return c
}
