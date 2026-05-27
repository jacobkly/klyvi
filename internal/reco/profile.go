package reco

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TasteProfile is the denormalized per-user feature weight bundle (§3.4).
// It is rebuilt from interactions and persisted so future feed requests
// avoid recomputing aggregates and so the frontend can read/display the
// profile for explainability and user-facing tuning.
type TasteProfile struct {
	UserID             uuid.UUID
	GenreWeights       map[int]float64
	KeywordWeights     map[int]float64
	EraWeights         map[int]float64 // keyed by decade (e.g. 1990, 2020)
	QualitySensitivity float64
	LikedCount         int
	DislikedCount      int
	UpdatedAt          time.Time
}

// ProfileRepository persists and loads taste_profiles rows.
type ProfileRepository interface {
	GetProfile(ctx context.Context, userID uuid.UUID) (*TasteProfile, error)
	UpsertProfile(ctx context.Context, p *TasteProfile) error
}

// BuildProfile aggregates the per-item liked/disliked signal into pooled
// feature weight maps. liked and disliked are the SplitLikedDisliked
// outputs already enriched with MediaFeatures by the orchestrator.
func BuildProfile(userID uuid.UUID, liked, disliked []SignalItem) *TasteProfile {
	p := &TasteProfile{
		UserID:         userID,
		GenreWeights:   pooledFeatureWeights(liked, genreKeyset),
		KeywordWeights: pooledFeatureWeights(liked, keywordKeyset),
		EraWeights:     pooledEraWeights(liked),
		LikedCount:     len(liked),
		DislikedCount:  len(disliked),
	}

	// QualitySensitivity is the signal-weighted average of the liked items'
	// vote_average. A user who consistently likes critically acclaimed films
	// gets a high value; one who likes everything regardless of rating gets
	// closer to the global mean.
	var qSum, wSum float64
	for _, it := range liked {
		if it.Features == nil || it.Features.VoteCount == 0 {
			continue
		}
		qSum += it.Features.VoteAverage * it.Weight
		wSum += it.Weight
	}
	if wSum > 0 {
		p.QualitySensitivity = qSum / wSum
	}
	return p
}
