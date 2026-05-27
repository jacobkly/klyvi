package reco

import (
	"context"
	"sort"
)

// Tier2 scores candidates against a persisted TasteProfile. The argument
// for Tier 2 over Tier 1: feed requests become a fast scored lookup
// against pre-aggregated weights rather than re-deriving them each call.
// In this implementation the profile is rebuilt from the orchestrator's
// already-loaded user signal on every request and persisted as a side
// effect — the speed win lands once the read path is optimized to skip
// feature-lookup when the profile is fresh.
type Tier2 struct {
	cfg  Config
	repo ProfileRepository
}

func NewTier2(cfg Config, repo ProfileRepository) *Tier2 {
	return &Tier2{cfg: cfg, repo: repo}
}

func (t *Tier2) Name() string { return "tier2" }

func (t *Tier2) Score(ctx context.Context, u *UserContext, cands []Candidate) ([]Scored, error) {
	if u == nil || len(u.Liked) == 0 {
		return nil, nil
	}

	profile := BuildProfile(u.UserID, u.Liked, u.Disliked)
	// Best-effort persistence — failing to save the profile does not block
	// the feed (the frontend just gets a slightly stale "your taste" view).
	_ = t.repo.UpsertProfile(ctx, profile)

	out := make([]Scored, 0, len(cands))
	for _, c := range cands {
		if c.Features == nil {
			continue
		}
		if c.Features.VoteCount < t.cfg.QualityFloorVotes {
			continue
		}

		gSim := overlapScore(c.Features.GenreIDs, profile.GenreWeights)
		kSim := overlapScore(c.Features.KeywordIDs, profile.KeywordWeights)
		eSim := eraSimilarity(c.Features.Year, profile.EraWeights)

		score := t.cfg.GenreWeight*gSim + t.cfg.KeywordWeight*kSim + t.cfg.EraWeight*eSim
		if score <= 0 {
			continue
		}

		out = append(out, Scored{
			Candidate: c,
			Score:     score,
			Reasons:   topReasons(c.Features, profile.KeywordWeights, profile.GenreWeights),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}
