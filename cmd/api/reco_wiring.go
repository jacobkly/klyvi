package main

import (
	"context"

	"github.com/google/uuid"

	"klyvi-api/internal/reco"
)

// emptyCatalog, emptySignal, emptySeen are placeholder reco-adapter
// implementations used while the recommender scaffolding is in place but
// the real scorers (Tier 0/1/2 in slices 4-6/4-7/4-8) and adapters are
// not yet built. They keep /v1/reco/feed live so the API surface is
// complete; the orchestrator short-circuits to an empty list because no
// Scorer is registered yet.
type emptyCatalog struct{}

func (emptyCatalog) SampleMovieCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	return nil, nil
}

type emptySignal struct{}

func (emptySignal) ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]reco.InteractionRow, error) {
	return nil, nil
}

type emptySeen struct{}

func (emptySeen) SeenMediaIDs(ctx context.Context, userID uuid.UUID) (map[int]bool, error) {
	return nil, nil
}
