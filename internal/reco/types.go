package reco

import (
	"github.com/google/uuid"
)

// MediaFeatures is the feature view of a media item the recommender uses
// at scoring time. Built from the `movies` cache (no precomputed table
// yet — see ARCHITECTURE §3.3 future direction). All TV-side fields are
// stubbed because Phase 4 is movies-only.
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
type Candidate struct {
	MediaID   int
	MediaType string
	Features  *MediaFeatures
}

// Scored is a ranked Candidate plus the score and human-readable reasons
// (used for paid-tier explanations and for harness/debug output).
type Scored struct {
	Candidate
	Score   float64
	Reasons []string
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

// Config holds the tunable constants from §5.2. All defaults can be
// overridden by the orchestrator's caller.
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
