package reco

import (
	"github.com/google/uuid"
)

// MediaFeatures is the feature view of a media item the recommender uses
// at scoring time. Built from the `movies` cache (no precomputed feature
// table yet — future work). All TV-side fields are stubbed because the
// movies recommender is built first; TV reco is later work.
type MediaFeatures struct {
	MediaID     int
	MediaType   string
	GenreIDs    []int
	KeywordIDs  []int
	Year        int // 0 if unknown
	VoteAverage float64
	VoteCount   int
}

// Candidate is a media item the orchestrator may rank for a user. Features
// can be nil for Tier 0 (cold start) — Tier 1+ require populated features.
//
// The display fields (TMDBID, Title, PosterPath, BackdropPath, ReleaseYear,
// VoteAverage) are populated by the catalog adapter so the feed handler can
// return everything the frontend needs to render a card without an N+1
// follow-up lookup. The scorers themselves do not read these fields — they
// ride through the embedding into Scored unchanged.
type Candidate struct {
	MediaID      int
	MediaType    string
	TMDBID       int
	Title        string
	PosterPath   string
	BackdropPath string
	ReleaseYear  int
	VoteAverage  float64
	Features     *MediaFeatures
}

// Scored is a ranked Candidate plus the score and structured reasons
// (used for paid-tier explanations and for harness/debug output).
type Scored struct {
	Candidate
	Score   float64
	Reasons []Reason
}

// Reason is a single explainability token attached to a Scored item.
// Kind is "keyword" or "genre"; ID is the TMDB feature id; Name is the
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

		NegativeSimWeight: 0.6,
		QualityFloorVotes: 25,
		GenreWeight:       0.3,
		KeywordWeight:     0.6,
		EraWeight:         0.1,

		MMRLambda: 0.7,
		FeedSize:  30,
	}
}
