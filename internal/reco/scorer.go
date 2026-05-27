package reco

import "context"

// Scorer is the seam that makes recommendation tiers pluggable (ARCHITECTURE
// §5.5). Each tier is a Scorer registered with the orchestrator. The
// orchestrator picks the active scorer (or a blend) based on the user's
// signal volume and the user's tier (free vs paid in future phases).
type Scorer interface {
	Name() string
	Score(ctx context.Context, u *UserContext, cands []Candidate) ([]Scored, error)
}
